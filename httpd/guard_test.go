package httpd_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rjbs/gizmo/httpd"
	"github.com/rjbs/gizmo/internal/bakery"
	"github.com/rjbs/gizmo/jmap"
)

// guardAnswers checks the status a guard gives a request for "/", by the
// given method, addressed to host, with the given headers, which are given
// as "Name: value".
func guardAnswers(t *testing.T, g *httpd.Guard, method, host string, headers []string, want int) {
	t.Helper()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	req := httptest.NewRequest(method, "/", nil)
	req.Host = host
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ": ")
		req.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	g.Handler(inner).ServeHTTP(w, req)
	if w.Code != want {
		t.Errorf("%s to %q with %v: status %d, want %d", method, host, headers, w.Code, want)
	}
}

func TestGuardHosts(t *testing.T) {
	g := httpd.NewGuard("", "https://photos.example.com:8443/", "jiggle")

	guardAnswers(t, g, "GET", "photos.example.com:8443", nil, 200)
	guardAnswers(t, g, "GET", "photos.example.com.", nil, 200)
	guardAnswers(t, g, "GET", "PHOTOS.example.com", nil, 200)
	guardAnswers(t, g, "GET", "jiggle", nil, 200)

	// These can't be made to mean some other machine.
	guardAnswers(t, g, "GET", "localhost:8739", nil, 200)
	guardAnswers(t, g, "GET", "LOCALHOST", nil, 200)
	guardAnswers(t, g, "GET", "127.0.0.1:8739", nil, 200)
	guardAnswers(t, g, "GET", "[::1]:8739", nil, 200)
	guardAnswers(t, g, "GET", "100.64.0.1", nil, 200)

	guardAnswers(t, g, "GET", "evil.example", nil, 421)
	guardAnswers(t, g, "GET", "evil.example:8739", nil, 421)
	guardAnswers(t, g, "GET", "photos.example.com.evil.example", nil, 421)
	guardAnswers(t, g, "GET", "localhost.evil.example", nil, 421)
	guardAnswers(t, g, "GET", "", nil, 421)

	guardAnswers(t, g, "GET", "late.example.ts.net", nil, 421)
	g.AllowHost("late.example.ts.net")
	guardAnswers(t, g, "GET", "late.example.ts.net", nil, 200)
}

func TestGuardOrigins(t *testing.T) {
	g := httpd.NewGuard("photos.example.com")
	host := "photos.example.com"

	// A client that isn't a browser sends neither header.
	guardAnswers(t, g, "POST", host, nil, 200)

	guardAnswers(t, g, "POST", host, []string{"Sec-Fetch-Site: same-origin"}, 200)
	guardAnswers(t, g, "POST", host, []string{"Sec-Fetch-Site: none"}, 200)
	guardAnswers(t, g, "POST", host, []string{"Sec-Fetch-Site: cross-site"}, 403)
	guardAnswers(t, g, "POST", host, []string{"Sec-Fetch-Site: same-site"}, 403)

	// An older browser sends only Origin.
	guardAnswers(t, g, "POST", host, []string{"Origin: https://photos.example.com"}, 200)
	guardAnswers(t, g, "POST", host, []string{"Origin: https://evil.example"}, 403)

	// A request that changes nothing can come from anywhere.
	guardAnswers(t, g, "GET", host, []string{"Sec-Fetch-Site: cross-site"}, 200)

	cross := []string{"Sec-Fetch-Site: cross-site", "Origin: https://editor.example"}
	guardAnswers(t, g, "POST", host, cross, 403)
	if err := g.TrustOrigin("https://editor.example"); err != nil {
		t.Fatal(err)
	}
	guardAnswers(t, g, "POST", host, cross, 200)
}

// serverAnswers checks the status the bakery, as a whole server, gives an
// API request addressed to host with the given header.
func serverAnswers(t *testing.T, b *bakery.Bakery, host, header string, want int) {
	t.Helper()
	req := httptest.NewRequest("POST", "/jmap/api", strings.NewReader(`{"using":[],"methodCalls":[]}`))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "application/json")
	if host != "" {
		req.Host = host
	}
	if name, value, ok := strings.Cut(header, ": "); ok {
		req.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, req)
	if w.Code != want {
		t.Errorf("a request to %q with %q: status %d, want %d: %s", host, header, w.Code, want, w.Body)
	}
}

// A Server that is given no Guard still has one.
func TestServerIsGuarded(t *testing.T) {
	b := bakery.New(jmap.Limits{})
	defer b.Close()

	serverAnswers(t, b, "127.0.0.1:8080", "", 200)
	serverAnswers(t, b, "localhost:8080", "Sec-Fetch-Site: same-origin", 200)
	serverAnswers(t, b, "localhost:8080", "Sec-Fetch-Site: cross-site", 403)
	serverAnswers(t, b, "evil.example", "", 421)
}
