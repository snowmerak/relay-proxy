// Package tcptunnel implements a TLS-passthrough TCP proxy.
//
// It listens on a TCP port (typically :443), peeks at the TLS ClientHello to
// extract the SNI hostname, matches the hostname against known relay domains,
// and tunnels the raw bytes to the relay backend without decrypting them.
//
// This means:
//   - relay-proxy never sees the plaintext HTTPS traffic
//   - the relay server's TLS certificate is presented directly to the browser
//   - any wildcard cert on the relay (e.g. *.portal.thumbgo.kr) covers the app subdomain
package tcptunnel

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"
)

// Server is a TLS-passthrough TCP proxy keyed by SNI.
type Server struct {
	addr   string
	dialer *net.Dialer
	relays func() []string // returns current relay domain list
}

// New creates a Server that listens on addr and forwards connections to relay
// backends using dialer for outbound TCP connections.
// relays is called on every new connection so the relay list stays current.
func New(addr string, dialer *net.Dialer, relays func() []string) *Server {
	return &Server{addr: addr, dialer: dialer, relays: relays}
}

// Run starts accepting connections. It blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	l, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	slog.Info("tls tunnel listening", "addr", s.addr)
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("tcptunnel: accept error", "err", err)
			continue
		}
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// Read enough of the TLS record to extract the SNI.
	// A typical ClientHello fits in 4 KiB; we never decrypt it.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || n == 0 {
		return
	}

	sni := extractSNI(buf[:n])
	if sni == "" {
		slog.Debug("tcptunnel: no SNI in ClientHello", "remote", conn.RemoteAddr())
		return
	}

	relayHost := s.findRelay(sni)
	if relayHost == "" {
		slog.Debug("tcptunnel: SNI does not match any relay", "sni", sni)
		return
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	backend, err := s.dialer.DialContext(dialCtx, "tcp", net.JoinHostPort(relayHost, "443"))
	if err != nil {
		slog.Warn("tcptunnel: dial backend failed", "relay", relayHost, "sni", sni, "err", err)
		return
	}
	defer backend.Close()

	slog.Debug("tcptunnel: tunneling", "sni", sni, "relay", relayHost)

	// Replay the peeked bytes to the backend before entering copy loop.
	if _, err := backend.Write(buf[:n]); err != nil {
		return
	}

	// Bidirectional copy. Each goroutine closes the opposite side when done so
	// the peer goroutine unblocks from its Read.
	done := make(chan struct{}, 2)
	go func() { io.Copy(backend, conn); conn.Close(); done <- struct{}{} }()
	go func() { io.Copy(conn, backend); backend.Close(); done <- struct{}{} }()
	<-done
	<-done
}

// findRelay returns the relay domain that the given SNI hostname belongs to.
func (s *Server) findRelay(sni string) string {
	for _, r := range s.relays() {
		if strings.HasSuffix(sni, "."+r) || sni == r {
			return r
		}
	}
	return ""
}

// extractSNI parses a TLS ClientHello record and returns the SNI server name.
// Returns "" if parsing fails or no SNI is present.
func extractSNI(b []byte) string {
	// TLS record header: content-type(1) + legacy-version(2) + length(2)
	if len(b) < 5 || b[0] != 0x16 { // 0x16 = handshake
		return ""
	}
	recLen := int(binary.BigEndian.Uint16(b[3:5]))
	if len(b) < 5+recLen {
		recLen = len(b) - 5 // partial read — parse what we have
	}
	d := b[5 : 5+recLen]

	// Handshake message: msg-type(1) + length(3) + client-version(2) + random(32)
	if len(d) < 4 || d[0] != 0x01 { // 0x01 = ClientHello
		return ""
	}
	pos := 1 + 3 + 2 + 32

	// Session ID
	if len(d) <= pos {
		return ""
	}
	pos += 1 + int(d[pos])

	// Cipher suites
	if len(d) < pos+2 {
		return ""
	}
	pos += 2 + int(binary.BigEndian.Uint16(d[pos:]))

	// Compression methods
	if len(d) < pos+1 {
		return ""
	}
	pos += 1 + int(d[pos])

	// Extensions
	if len(d) < pos+2 {
		return "" // no extensions
	}
	extEnd := pos + 2 + int(binary.BigEndian.Uint16(d[pos:]))
	pos += 2
	if extEnd > len(d) {
		extEnd = len(d)
	}

	for pos+4 <= extEnd {
		extType := binary.BigEndian.Uint16(d[pos:])
		extLen := int(binary.BigEndian.Uint16(d[pos+2:]))
		pos += 4

		if extType == 0x0000 && extLen >= 5 { // server_name extension
			// server_name_list_length(2) + name_type(1) + name_length(2) + name
			if d[pos+2] == 0x00 { // host_name
				nameLen := int(binary.BigEndian.Uint16(d[pos+3:]))
				end := pos + 5 + nameLen
				if end <= extEnd {
					return string(d[pos+5 : end])
				}
			}
		}
		pos += extLen
	}
	return ""
}
