// Package dnsserver implements a minimal DNS server that:
//   - Returns A records pointing to selfIP for wildcard relay domain queries
//   - Forwards all other queries to an upstream resolver
//
// No external dependencies: uses only the standard library and manual
// DNS wire-format encoding/decoding.
package dnsserver

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	dnsTypeA   = 1
	dnsClassIN = 1
	dnsTTL     = 60 // seconds
	maxUDPSize = 512
)

// Server is a UDP DNS server.
type Server struct {
	addr      string
	upstreams []string // tried in order; falls back to next on error
	selfIP    net.IP

	mu      sync.RWMutex
	domains []string // relay domain suffixes, e.g. "portal.thumbgo.kr"
}

// New creates a Server. selfIP is the IPv4 address returned for wildcard relay queries.
// upstreams is a list of upstream DNS servers; the first reachable one is used.
func New(addr string, upstreams []string, selfIP string) (*Server, error) {
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("at least one upstream DNS server is required")
	}
	ip := net.ParseIP(selfIP).To4()
	if ip == nil {
		return nil, fmt.Errorf("invalid selfIP %q (must be IPv4)", selfIP)
	}
	return &Server{addr: addr, upstreams: upstreams, selfIP: ip}, nil
}

// SetDomains replaces the list of relay domain suffixes.
func (s *Server) SetDomains(domains []string) {
	lower := make([]string, len(domains))
	for i, d := range domains {
		lower[i] = strings.ToLower(strings.TrimSuffix(d, "."))
	}
	s.mu.Lock()
	s.domains = lower
	s.mu.Unlock()
}

// Run listens on UDP and serves DNS queries until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	pc, err := net.ListenPacket("udp", s.addr)
	if err != nil {
		return fmt.Errorf("dns listen %s: %w", s.addr, err)
	}
	slog.Info("dns server listening", "addr", s.addr)

	go func() {
		<-ctx.Done()
		pc.Close()
	}()

	buf := make([]byte, maxUDPSize)
	for {
		n, src, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("dns read error", "err", err)
			continue
		}
		msg := make([]byte, n)
		copy(msg, buf[:n])
		go s.handle(ctx, pc, src, msg)
	}
}

func (s *Server) handle(ctx context.Context, pc net.PacketConn, src net.Addr, msg []byte) {
	if len(msg) < 12 {
		return
	}

	flags := binary.BigEndian.Uint16(msg[2:4])
	// Only handle standard queries (QR=0, Opcode=0000).
	if flags&0x8000 != 0 || flags&0x7800 != 0 {
		return
	}
	qdcount := binary.BigEndian.Uint16(msg[4:6])
	if qdcount == 0 {
		return
	}

	name, afterName, err := parseName(msg, 12)
	if err != nil || afterName+4 > len(msg) {
		return
	}
	qtype := binary.BigEndian.Uint16(msg[afterName : afterName+2])
	afterQuestion := afterName + 4 // skip QTYPE + QCLASS

	name = strings.ToLower(strings.TrimSuffix(name, "."))

	// For A queries that match a relay domain, answer directly.
	if qtype == dnsTypeA && s.matchesDomain(name) {
		resp := buildAResponse(msg, afterQuestion, s.selfIP)
		if resp != nil {
			_, _ = pc.WriteTo(resp, src)
			slog.Debug("dns answered", "name", name, "ip", s.selfIP)
			return
		}
	}

	// Forward to upstream, trying each in order.
	resp, err := forwardWithFallback(ctx, s.upstreams, msg)
	if err != nil {
		slog.Warn("dns forward failed", "name", name, "err", err)
		return
	}
	_, _ = pc.WriteTo(resp, src)
}

func (s *Server) matchesDomain(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.domains {
		if strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

// buildAResponse constructs a DNS A record response from the original query.
// query[:afterQuestion] is the header + question section to echo back.
func buildAResponse(query []byte, afterQuestion int, ip net.IP) []byte {
	ip = ip.To4()
	if ip == nil {
		return nil
	}

	// Response = echoed header+question (afterQuestion bytes) + one RR (16 bytes).
	resp := make([]byte, afterQuestion+16)
	copy(resp, query[:afterQuestion])

	// Update header flags: QR=1, AA=1, RD=copy, RA=1, RCODE=0.
	origFlags := binary.BigEndian.Uint16(query[2:4])
	newFlags := uint16(0x8400) | (origFlags & 0x0100) | 0x0080
	binary.BigEndian.PutUint16(resp[2:4], newFlags)
	binary.BigEndian.PutUint16(resp[4:6], 1)   // QDCOUNT
	binary.BigEndian.PutUint16(resp[6:8], 1)   // ANCOUNT
	binary.BigEndian.PutUint16(resp[8:10], 0)  // NSCOUNT
	binary.BigEndian.PutUint16(resp[10:12], 0) // ARCOUNT

	// Answer RR.
	o := afterQuestion
	resp[o] = 0xC0   // compression pointer …
	resp[o+1] = 0x0C // … to offset 12 (start of QNAME in question)
	o += 2
	binary.BigEndian.PutUint16(resp[o:], dnsTypeA)
	o += 2
	binary.BigEndian.PutUint16(resp[o:], dnsClassIN)
	o += 2
	binary.BigEndian.PutUint32(resp[o:], dnsTTL)
	o += 4
	binary.BigEndian.PutUint16(resp[o:], 4) // RDLENGTH
	o += 2
	copy(resp[o:], ip)

	return resp
}

// forwardWithFallback tries each upstream in order and returns the first successful response.
func forwardWithFallback(ctx context.Context, upstreams []string, msg []byte) ([]byte, error) {
	var lastErr error
	for _, upstream := range upstreams {
		resp, err := forward(ctx, upstream, msg)
		if err == nil {
			return resp, nil
		}
		slog.Debug("upstream failed, trying next", "upstream", upstream, "err", err)
		lastErr = err
	}
	return nil, fmt.Errorf("all upstreams failed; last error: %w", lastErr)
}

// forward sends msg to a single upstream DNS server over UDP and returns the response.
func forward(ctx context.Context, upstream string, msg []byte) ([]byte, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "udp", upstream)
	if err != nil {
		return nil, fmt.Errorf("dial upstream: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write(msg); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	buf := make([]byte, maxUDPSize)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return buf[:n], nil
}

// parseName reads a DNS name (with compression pointer support) from msg at offset.
// Returns the decoded name and the offset immediately after the name in the original message.
func parseName(msg []byte, offset int) (string, int, error) {
	var labels []string
	origOffset := -1 // offset after the first compression pointer encountered
	visited := make(map[int]bool)

	for {
		if offset >= len(msg) {
			return "", 0, fmt.Errorf("dns: unexpected end at offset %d", offset)
		}
		length := int(msg[offset])
		if length == 0 {
			offset++
			break
		}
		if length&0xC0 == 0xC0 {
			// Compression pointer.
			if offset+2 > len(msg) {
				return "", 0, fmt.Errorf("dns: truncated pointer at %d", offset)
			}
			if origOffset == -1 {
				origOffset = offset + 2
			}
			ptr := int(binary.BigEndian.Uint16(msg[offset:offset+2])) & 0x3FFF
			if visited[ptr] || ptr >= offset {
				return "", 0, fmt.Errorf("dns: bad compression pointer %d", ptr)
			}
			visited[ptr] = true
			offset = ptr
			continue
		}
		offset++
		if offset+length > len(msg) {
			return "", 0, fmt.Errorf("dns: label exceeds message at %d", offset)
		}
		labels = append(labels, string(msg[offset:offset+length]))
		offset += length
	}

	if origOffset != -1 {
		offset = origOffset
	}
	return strings.Join(labels, "."), offset, nil
}
