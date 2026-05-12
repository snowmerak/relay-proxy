package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
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

// Forward proxies the request to the given relay under the given appName path.
func (rp *ReverseProxy) Forward(w http.ResponseWriter, r *http.Request, relay *registry.Relay, appName string) {
	target := relayTarget(relay, appName)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
			// Preserve original Host so the relay can route correctly.
			req.Out.Host = fmt.Sprintf("%s.%s", appName, relay.ID)
		},
	}

	start := time.Now()
	crw := &captureResponseWriter{ResponseWriter: w, status: http.StatusOK}
	proxy.ServeHTTP(crw, r)
	rp.balancer.RecordLatency(relay.ID, time.Since(start))
}

func relayTarget(relay *registry.Relay, appName string) *url.URL {
	u := *relay.BaseURL
	u.Host = fmt.Sprintf("%s.%s", appName, relay.ID)
	return &u
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
