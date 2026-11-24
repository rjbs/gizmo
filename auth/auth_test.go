package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rjbs/gizmo/jmap"
)

const testPrefix = "gizmotest-"

// testDirectory has two users.  alice owns "home" and "work", and holds the
// loopback credential, a tailscale login, a bearer token for both accounts
// and another limited to "work".  bob owns "bobs" and holds a tailscale
// login.  It returns alice's two tokens along with the directory.
func testDirectory(t *testing.T) (d *Directory, full, workOnly string) {
	t.Helper()
	full, _ = NewToken(testPrefix)
	workOnly, _ = NewToken(testPrefix)
	users := map[string]*User{
		"alice": {
			PrimaryAccount: "home",
			Credentials: []Credential{
				{Type: CredLoopback},
				{Type: CredTailscale, Login: "alice@example"},
				{Type: CredBearer, TokenHash: HashToken(full)},
				{Type: CredBearer, TokenHash: HashToken(workOnly), AccountIDs: []string{"work"}},
			},
		},
		"bob": {Credentials: []Credential{{Type: CredTailscale, Login: "bob@example"}}},
	}
	accounts := map[string]*Account{
		"home": {Owner: "alice"},
		"work": {Owner: "alice"},
		"bobs": {Owner: "bob"},
	}
	d, err := NewDirectory(users, accounts)
	if err != nil {
		t.Fatal(err)
	}
	d.TokenPrefix = testPrefix
	return d, full, workOnly
}

// whoIs stands in for Tailscale: the remote address is the login, and one
// beginning "tag:" is a tagged node.
func whoIs(_ context.Context, remoteAddr string) (Identity, error) {
	if remoteAddr == "" {
		return Identity{}, errors.New("nobody")
	}
	return Identity{Login: remoteAddr, Tagged: strings.HasPrefix(remoteAddr, "tag:")}, nil
}

// checkAuth sends a request through the middleware for a listener, "plain"
// or "tailnet", from the given tailnet login and with the given
// Authorization header, and checks the status and who it was served as:
// "user:account,account;primary", or "" when it was refused.
func checkAuth(t *testing.T, name string, d *Directory, listener, login, authorization string, wantStatus int, wantAs string) {
	t.Helper()
	served := ""
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := UserFrom(r.Context())
		served = u.ID + ":" + strings.Join(u.Accounts, ",") + ";" + u.PrimaryAccount
	})
	reject := func(*http.Request, string) {}
	var h http.Handler
	switch listener {
	case "plain":
		h = Bearer(d, reject, Loopback(d, inner))
	case "tailnet":
		h = Bearer(d, reject, Tailscale(d, whoIs, reject, inner))
	default:
		t.Fatalf("no listener %q", listener)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = login
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != wantStatus || served != wantAs {
		t.Errorf("%s: got status %d as %q, want %d as %q", name, w.Code, served, wantStatus, wantAs)
	}
}

func TestMiddleware(t *testing.T) {
	d, full, workOnly := testDirectory(t)
	other, _ := NewToken(testPrefix)

	checkAuth(t, "plain listener, no token", d, "plain", "", "", 200, "alice:home,work;home")
	checkAuth(t, "plain listener, token", d, "plain", "", "Bearer "+full, 200, "alice:home,work;home")
	checkAuth(t, "a token limited to one account", d, "plain", "", "Bearer "+workOnly, 200, "alice:work;")
	checkAuth(t, "the scheme in any case", d, "plain", "", "bearer "+full, 200, "alice:home,work;home")
	checkAuth(t, "a token nobody holds", d, "plain", "", "Bearer "+other, 401, "")
	checkAuth(t, "a malformed token", d, "plain", "", "Bearer nonsense", 401, "")
	checkAuth(t, "some other scheme is left to the listener", d, "plain", "", "Basic abc", 200, "alice:home,work;home")

	checkAuth(t, "tailnet login", d, "tailnet", "bob@example", "", 200, "bob:bobs;bobs")
	checkAuth(t, "tailnet login nobody holds", d, "tailnet", "eve@example", "", 403, "")
	checkAuth(t, "tagged node", d, "tailnet", "tag:server", "", 403, "")
	checkAuth(t, "unidentified node", d, "tailnet", "", "", 401, "")
	checkAuth(t, "a token wins over the tailnet login", d, "tailnet", "bob@example", "Bearer "+workOnly, 200, "alice:work;")
	checkAuth(t, "a bad token isn't rescued by the tailnet login", d, "tailnet", "bob@example", "Bearer "+other, 401, "")

	nobody, err := NewDirectory(
		map[string]*User{"bob": {Credentials: []Credential{{Type: CredTailscale, Login: "bob@example"}}}},
		map[string]*Account{"bobs": {Owner: "bob"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	checkAuth(t, "plain listener with no loopback user", nobody, "plain", "", "", 401, "")
}

// checkBadDirectory checks that a directory is refused, with an error
// mentioning want.
func checkBadDirectory(t *testing.T, name string, users map[string]*User, accounts map[string]*Account, want string) {
	t.Helper()
	_, err := NewDirectory(users, accounts)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s: got error %v, want one mentioning %q", name, err, want)
	}
}

func TestBadDirectories(t *testing.T) {
	loopback := []Credential{{Type: CredLoopback}}
	checkBadDirectory(t, "unknown owner",
		map[string]*User{}, map[string]*Account{"a": {Owner: "ghost"}}, "unknown user")
	checkBadDirectory(t, "a user with no accounts",
		map[string]*User{"u": {}}, map[string]*Account{}, "owns no accounts")
	checkBadDirectory(t, "a primary account that isn't the user's",
		map[string]*User{"u": {PrimaryAccount: "b"}, "v": {}},
		map[string]*Account{"a": {Owner: "u"}, "b": {Owner: "v"}}, "does not own")
	checkBadDirectory(t, "loopback held twice",
		map[string]*User{"u": {Credentials: loopback}, "v": {Credentials: loopback}},
		map[string]*Account{"a": {Owner: "u"}, "b": {Owner: "v"}}, "both claim")
	checkBadDirectory(t, "a credential for nothing",
		map[string]*User{"u": {Credentials: []Credential{{Type: CredLoopback, AccountIDs: []string{}}}}},
		map[string]*Account{"a": {Owner: "u"}}, "empty accountIds")
	checkBadDirectory(t, "a token hash that isn't one",
		map[string]*User{"u": {Credentials: []Credential{{Type: CredBearer, TokenHash: "abc"}}}},
		map[string]*Account{"a": {Owner: "u"}}, "tokenHash")
	checkBadDirectory(t, "an unknown kind of credential",
		map[string]*User{"u": {Credentials: []Credential{{Type: "password"}}}},
		map[string]*Account{"a": {Owner: "u"}}, "unknown type")
}

// checkRoute calls a method made by ForAccount as the given user, "" for
// nobody, with the given arguments, and checks what comes back: the name of
// the backend that handled it, or the type of the method error.
func checkRoute(t *testing.T, name string, d *Directory, as, args, want string) {
	t.Helper()
	backends := map[string]string{"home": "home's", "work": "work's", "bobs": "bob's"}
	h := ForAccount(
		func(id string) (string, bool) { b, ok := backends[id]; return b, ok },
		func(b string, _ *jmap.Call, _ json.RawMessage) (any, error) {
			return map[string]any{"backend": b}, nil
		},
	)
	p := jmap.NewProcessor()
	p.Register("Thing/get", "https://gizmo.example/test", h)

	ctx := context.Background()
	if as != "" {
		ctx = WithUser(ctx, d.Users[as])
	}
	req, rerr := jmap.ParseRequest([]byte(`{"using":["https://gizmo.example/test"],"methodCalls":[["Thing/get",` + args + `,"a"]]}`))
	if rerr != nil {
		t.Fatal(rerr)
	}
	resp, rerr := p.Process(ctx, req)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var out struct{ Backend, Type string }
	if err := json.Unmarshal(resp.MethodResponses[0].Args, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.Backend + out.Type; got != want {
		t.Errorf("%s: got %q, want %q", name, got, want)
	}
}

func TestForAccount(t *testing.T) {
	d, _, _ := testDirectory(t)
	d.Accounts["attic"] = &Account{ID: "attic", Owner: "alice"}
	d.Users["alice"].Accounts = []string{"attic", "home", "work"}

	checkRoute(t, "an account of the caller's", d, "alice", `{"accountId":"work"}`, "work's")
	checkRoute(t, "someone else's account", d, "alice", `{"accountId":"bobs"}`, "accountNotFound")
	checkRoute(t, "an account that doesn't exist", d, "alice", `{"accountId":"nope"}`, "accountNotFound")
	checkRoute(t, "an account with no backend", d, "alice", `{"accountId":"attic"}`, "accountNotFound")
	checkRoute(t, "no account named", d, "alice", `{}`, "invalidArguments")
	checkRoute(t, "an account that isn't a string", d, "alice", `{"accountId":7}`, "invalidArguments")
	checkRoute(t, "nobody calling", d, "", `{"accountId":"home"}`, "accountNotFound")
}

func TestSolo(t *testing.T) {
	d := Solo("juke", "music")
	checkAuth(t, "the one user, from anywhere", d, "plain", "", "", 200, "juke:music;music")
	checkAuth(t, "the one user has no token", d, "plain", "", "Bearer "+mustToken(t), 401, "")
	if a := d.Accounts["music"]; a.Owner != "juke" || a.Name != "juke" {
		t.Errorf("the one account is %+v", a)
	}
}

func mustToken(t *testing.T) string {
	t.Helper()
	token, err := NewToken("")
	if err != nil {
		t.Fatal(err)
	}
	return token
}
