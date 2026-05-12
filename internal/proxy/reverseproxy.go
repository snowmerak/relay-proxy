package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"

	"github.com/snowmerak/relay-proxy/internal/registry"
)

// ReverseProxy wraps httputil.ReverseProxy.
type ReverseProxy struct{}

func NewReverseProxy() *ReverseProxy {
	return &ReverseProxy{}
}

// Forward proxies the request to the given relay for the given appName.
// TCP always connects to relay.BaseURL directly; only the Host header is set to
// {appName}.{relayID} so the relay can route to the correct tunnel.
// Returns the upstream HTTP status code. On >=500 the response is NOT flushed
// to w so the caller can return an error response.
func (rp *ReverseProxy) Forward(w http.ResponseWriter, r *http.Request, relay *registry.Relay, appName string) int {
	target := *relay.BaseURL

	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(&target)
			req.Out.Host = fmt.Sprintf("%s.%s", appName, relay.ID)
		},
	}

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, r)

	if rec.Code >= 500 {
		return rec.Code
	}

	flushRecorder(w, rec)
	return rec.Code
}

// ForwardRoot proxies a request to the relay root (no appName).
func (rp *ReverseProxy) ForwardRoot(w http.ResponseWriter, r *http.Request, relay *registry.Relay) {
	target := *relay.BaseURL

	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(&target)
			req.Out.Host = relay.ID
		},
	}

	proxy.ServeHTTP(w, r)
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
