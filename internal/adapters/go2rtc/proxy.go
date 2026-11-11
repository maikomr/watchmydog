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
	}
	return p
}
