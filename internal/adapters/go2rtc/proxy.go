package go2rtc

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// newProxy returns a reverse proxy to go2rtc's API/player endpoints. The
// HTTP layer mounts it under a prefix (e.g. /media/) and strips the prefix
// before handing requests over, so "/media/api/webrtc" reaches go2rtc as
// "/api/webrtc". WebSocket upgrades (go2rtc's /api/ws signaling) pass
// through because httputil.ReverseProxy supports them.
func newProxy(apiAddr string) http.Handler {
	target := &url.URL{Scheme: "http", Host: apiAddr}
	p := httputil.NewSingleHostReverseProxy(target)
	director := p.Director
	p.Director = func(r *http.Request) {
		director(r)
		r.Host = target.Host
		// go2rtc's WebSocket handler rejects requests whose Origin host does
		// not match the Host header (403). Browsers send the public origin
		// (e.g. http://maikos-mac-mini:8080), so present it as local. Origin
		// policy for our own API is enforced in the HTTP layer, not here.
		if r.Header.Get("Origin") != "" {
			r.Header.Set("Origin", target.String())
		}
	}
	return p
}
