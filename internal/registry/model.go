package registry

import "net/url"

// Relay represents a single relay server instance.
type Relay struct {
	ID      string   // domain, e.g. "portal.thumbgo.kr"
	BaseURL *url.URL // e.g. https://portal.thumbgo.kr
}
