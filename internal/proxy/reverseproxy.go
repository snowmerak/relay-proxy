package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"time"

	"github.com/snowmerak/relay-proxy/internal/balancer"
	"github.com/snowmerak/relay-proxy/internal/registry"
)

// ReverseProxy wraps httputil.ReverseProxy and records latency to the balancer.
type ReverseProxy struct {
	balancer *balancer.RoundRobin
}

func NewReverseProxy(b *balancer.RoundRobin) *ReverseProxy {
	return &ReverseProxy{balancer: b}
}

// Forward proxies the request to the given relay for the given appName.
//
// The TCP connection always goes to relay.BaseURL so that relay-proxy's own
// DNS server (if enabled) does not resolve {appName}.{relayID} back to
// 127.0.0.1 and cause an infinite loop. Only the Host header is set to
// {appName}.{relayID} so the relay can route to the correct tunnel.
//
// The response is buffered into a recorder; the caller inspects the status
// code and decides whether to flush to the real w or retry another relay.
func (rp *ReverseProxy) Forward(w http.ResponseWriter, r *http.Request, relay *registry.Relay, appName string) int {
	// Connect to the relay's actual address, not to {appName}.{relayID}.
	target := *relay.BaseURL

	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(&target)
			// Tell the relay which app to route to via the Host header.
			req.Out.Host = fmt.Sprintf("%s.%s", appName, relay.ID)
		},
	}

	start := time.Now()
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, r)
	rp.balancer.RecordLatency(relay.ID, time.Since(start))

	if rec.Code >= 500 {
		// Don't flush to w yet — let the caller decide to retry.
		return rec.Code
	}

	// Flush buffered response to the real ResponseWriter.
	flushRecorder(w, rec)
	return rec.Code
}

// ForwardRoot proxies a request destined for the relay root domain (no appName).
func (rp *ReverseProxy) ForwardRoot(w http.ResponseWriter, r *http.Request, relay *registry.Relay) {
	target := *relay.BaseURL

	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(&target)
			req.Out.Host = relay.ID
		},
	}

	start := time.Now()
	proxy.ServeHTTP(w, r)
	rp.balancer.RecordLatency(relay.ID, time.Since(start))
}

// flushRecorder copies a buffered ResponseRecorder to a real ResponseWriter.
func flushRecorder(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, vs := range rec.Header() {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.Code)
	_, _ = rec.Body.WriteTo(w)
}

// captureResponseWriter captures the status code for latency recording purposes.
type captureResponseWriter struct {
	http.ResponseWriter
	status int
}

func (c *captureResponseWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}
