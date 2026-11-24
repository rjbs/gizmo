package httpd

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
)

// A Guard keeps a web browser from being used against the server.
//
// A server may decide who a request is from by where it comes from: this
// machine, say, or a node of a tailnet that the right person is logged in
// to.  A browser there will send requests for any page that asks it to, so
// coming from the right place is not enough.  The Guard refuses two kinds
// of request, neither of which a client that isn't a browser will make.
//
// The first is a request that could change something, sent by a browser
// for a page from another origin.  The API reads a request's body whatever
// its content type, so with no guard any page open in the browser could
// make method calls, though it couldn't read the answers.  These are
// refused with 403, by the standard library's CrossOriginProtection.
//
// The second is a request addressed to a name that isn't the server's.  A
// page from anywhere can have its own name made to resolve to the server's
// address, and then the browser takes the server for the page's own site,
// and lets the page read whatever the server will send.  Such a request
// still says whose name it was sent to, and that is how it's caught.  It
// is refused with 421.  A request addressed to an IP address or to
// "localhost" is always let by, because those can't be made to mean some
// other machine.
//
// A Server guards itself.  To guard what an application serves beside it,
// give the Server's Guard the application's handler.
// -- claude, 2026-11-23
type Guard struct {
	origins *http.CrossOriginProtection

	mu    sync.RWMutex
	hosts map[string]bool
}

// NewGuard returns a Guard for a server known by the names given.
func NewGuard(names ...string) *Guard {
	g := &Guard{origins: http.NewCrossOriginProtection(), hosts: map[string]bool{"localhost": true}}
	g.AllowHost(names...)
	return g
}

// AllowHost adds to the names the server is known by.  Each may be a host
// name or a URL that has one; empty names are ignored.  It may be called
// while the server is serving, for a name that is learned late, like a
// tailnet node's.
func (g *Guard) AllowHost(names ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, name := range names {
		if u, err := url.Parse(name); err == nil && u.Host != "" {
			name = u.Hostname()
		}
		if name = bareHost(name); name != "" {
			g.hosts[name] = true
		}
	}
}

// TrustOrigin lets pages from another origin, given like
// "https://example.com", make requests that could change something.  It is
// for a web client served from somewhere other than the server.
func (g *Guard) TrustOrigin(origin string) error {
	return g.origins.AddTrustedOrigin(origin)
}

// Handler returns a handler that passes to next the requests the Guard
// lets by.
func (g *Guard) Handler(next http.Handler) http.Handler {
	return g.origins.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = bareHost(host)
		if _, err := netip.ParseAddr(host); err != nil && !g.knows(host) {
			http.Error(w, "this server is not known by the name "+host, http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (g *Guard) knows(host string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.hosts[host]
}

// bareHost puts a host name in the one form names are compared in.
func bareHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
}
