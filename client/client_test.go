package client_test

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rjbs/gizmo/client"
	"github.com/rjbs/gizmo/internal/bakery"
	"github.com/rjbs/gizmo/jmap"
)

var ctx = context.Background()

// connect starts a bakery and returns a client of it that carries the given
// bearer token, which is one of "alice", "cellar", "bob", or "" for none.
func connect(t *testing.T, who string) *client.Client {
	t.Helper()
	b := bakery.New(jmap.Limits{MaxSizeUpload: 1 << 20})
	t.Cleanup(b.Close)
	web := httptest.NewServer(b.Handler)
	t.Cleanup(web.Close)

	c := client.New(web.URL, jmap.CoreCapability, bakery.Capability)
	token := map[string]string{"alice": b.AliceToken, "cellar": b.CellarToken, "bob": b.BobToken}[who]
	if token != "" {
		c.Authorization = "Bearer " + token
	}
	return c
}

// checkAccount checks which account a client is given as its primary one
// for the bakery, or, if wantError is not empty, what the error says.
func checkAccount(t *testing.T, who, wantUser, wantAccount, wantError string) {
	t.Helper()
	c := connect(t, who)
	err := c.UseAccount(ctx, bakery.Capability)
	if wantError != "" {
		if err == nil || !strings.Contains(err.Error(), wantError) {
			t.Errorf("as %q: error %v, want one about %q", who, err, wantError)
		}
		return
	}
	if err != nil {
		t.Fatalf("as %q: %v", who, err)
	}
	s, _ := c.Session(ctx)
	if s.Username != wantUser || c.AccountID != wantAccount {
		t.Errorf("as %q: user %q with account %q, want %q with %q", who, s.Username, c.AccountID, wantUser, wantAccount)
	}
}

func TestSession(t *testing.T) {
	checkAccount(t, "alice", "alice", "kitchen", "")
	checkAccount(t, "", "alice", "kitchen", "")
	checkAccount(t, "bob", "bob", "bobs", "")

	// The token that sees only the cellar has no primary account, the
	// kitchen being out of its sight.
	checkAccount(t, "cellar", "", "", "no primary account")
}

func TestSessionElsewhere(t *testing.T) {
	c := connect(t, "alice")
	c.SessionURL = "/jmap/session"
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.APIURL, c.BaseURL) || !strings.HasSuffix(s.APIURL, "/jmap/api") {
		t.Errorf("the API is at %q, which isn't under %q", s.APIURL, c.BaseURL)
	}

	c = client.New("http://127.0.0.1:1")
	if _, err := c.Session(ctx); err == nil {
		t.Error("a session was found where there is no server")
	}
	c = client.New("nowhere")
	if _, err := c.Session(ctx); err == nil || !strings.Contains(err.Error(), "not the URL of a server") {
		t.Errorf("a base URL with no host: %v", err)
	}
}

// kitchen returns alice's client, with the kitchen as its account.
func kitchen(t *testing.T) *client.Client {
	t.Helper()
	c := connect(t, "alice")
	if err := c.UseAccount(ctx, bakery.Capability); err != nil {
		t.Fatal(err)
	}
	return c
}

// bake creates a cookie of each flavor with one Cookie/set, and returns
// their ids in the same order.
func bake(t *testing.T, c *client.Client, flavors ...string) []string {
	t.Helper()
	create := client.Args{}
	for _, f := range flavors {
		create[f] = client.Args{"flavor": f}
	}
	var set struct {
		Created map[string]struct{ ID string }
	}
	if err := c.Call(ctx, "Cookie/set", client.Args{"create": create}, &set); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(flavors))
	for i, f := range flavors {
		if ids[i] = set.Created[f].ID; ids[i] == "" {
			t.Fatalf("no %s cookie was created", f)
		}
	}
	return ids
}

// checkCallError checks that one method call fails with a method-level
// error of the given type.
func checkCallError(t *testing.T, c *client.Client, method string, args client.Args, wantType string) {
	t.Helper()
	err := c.Call(ctx, method, args, nil)
	if !client.IsMethodError(err, wantType) {
		t.Errorf("%s: error %v, want a method error of type %s", method, err, wantType)
		return
	}
	var failed *client.MethodError
	errors.As(err, &failed)
	if failed.Method != method || !strings.HasPrefix(failed.Error(), method+": "+wantType) {
		t.Errorf("%s: the error is of method %q and reads %q", method, failed.Method, failed)
	}
}

func TestCall(t *testing.T) {
	c := kitchen(t)
	ids := bake(t, c, "ginger", "oat")

	var got struct {
		AccountID string
		List      []struct{ ID, Flavor string }
	}
	if err := c.Call(ctx, "Cookie/get", client.Args{"ids": ids[:1]}, &got); err != nil {
		t.Fatal(err)
	}
	if got.AccountID != "kitchen" || len(got.List) != 1 || got.List[0].Flavor != "ginger" {
		t.Errorf("Cookie/get gave %+v", got)
	}

	checkCallError(t, c, "Cookie/get", client.Args{"ids": 7}, "invalidArguments")
	checkCallError(t, c, "Cookie/eat", nil, "unknownMethod")
	checkCallError(t, c, "Cookie/get", client.Args{"accountId": "bobs"}, "accountNotFound")
	checkCallError(t, c, "Cookie/get", client.Args{"accountId": client.Omit}, "invalidArguments")
	checkCallError(t, c, "Cookie/set", client.Args{"ifInState": "stale"}, "stateMismatch")

	// An error that isn't a method's is not taken for one.
	if client.IsMethodError(errors.New("stateMismatch"), "stateMismatch") {
		t.Error("a plain error was taken for a method error")
	}
}

func TestRequest(t *testing.T) {
	c := kitchen(t)
	bake(t, c, "ginger", "oat", "rye")

	resp, err := c.Request(ctx,
		client.Do("Cookie/query", client.Args{"sort": []client.Args{{"property": "flavor"}}}).As("q"),
		client.Do("Cookie/get", client.Args{"#ids": client.Ref("q", "Cookie/query", "/ids"), "properties": []string{"flavor"}}).As("g"),
		client.Do("Cookie/eat", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 3 || resp.SessionState == "" {
		t.Fatalf("%d results, with session state %q", len(resp.Results), resp.SessionState)
	}

	var got struct{ List []struct{ Flavor string } }
	if of := resp.For("g"); len(of) != 1 {
		t.Fatalf("%d results for the call g", len(of))
	} else if err := of[0].Decode(&got); err != nil {
		t.Fatal(err)
	}
	var flavors []string
	for _, ck := range got.List {
		flavors = append(flavors, ck.Flavor)
	}
	if strings.Join(flavors, " ") != "ginger oat rye" {
		t.Errorf("the cookies fetched by reference are %v", flavors)
	}

	// A call given no id is known by its place among the calls.
	if of := resp.For("2"); len(of) != 1 || !client.IsMethodError(of[0].Err(), "unknownMethod") {
		t.Errorf("the third call's results: %v", of)
	}
	if !client.IsMethodError(resp.Err(), "unknownMethod") {
		t.Errorf("the response's first error: %v", resp.Err())
	}
	if err := resp.For("2")[0].Decode(&got); !client.IsMethodError(err, "unknownMethod") {
		t.Errorf("decoding an error: %v", err)
	}
}

// checkRefused checks that err is a request the server refused with the
// given status and, if wantType is not empty, a problem of that type.
func checkRefused(t *testing.T, name string, err error, wantStatus int, wantType string) {
	t.Helper()
	var refused *client.RequestError
	if !errors.As(err, &refused) {
		t.Errorf("%s: error %v, want a refused request", name, err)
		return
	}
	if refused.Status != wantStatus || !strings.HasSuffix(refused.Type, wantType) {
		t.Errorf("%s: refused with status %d and type %q, want %d and %q", name, refused.Status, refused.Type, wantStatus, wantType)
	}
}

func TestRefusals(t *testing.T) {
	c := kitchen(t)
	c.Using = []string{"https://example.com/no-such-thing"}
	_, err := c.Request(ctx, client.Do("Core/echo", nil))
	checkRefused(t, "an unknown capability", err, 400, ":unknownCapability")
	checkRefused(t, "the same by Call", c.Call(ctx, "Core/echo", nil, nil), 400, ":unknownCapability")

	c = connect(t, "alice")
	c.Authorization = "Bearer " + bakery.TokenPrefix + "not-a-token"
	_, err = c.Session(ctx)
	checkRefused(t, "a bad token", err, 401, "")
}

func TestBlobs(t *testing.T) {
	c := kitchen(t)
	recipe := "flour, butter, sugar"

	// sizes: one known, and one not, which goes in chunks.
	for _, size := range []int64{int64(len(recipe)), -1} {
		up, err := c.Upload(ctx, "text/plain", strings.NewReader(recipe), size)
		if err != nil {
			t.Fatal(err)
		}
		if up.Size != int64(len(recipe)) || up.Type != "text/plain" || up.AccountID != "kitchen" {
			t.Errorf("uploaded with size %d: %+v", size, up)
		}

		body, err := c.Download(ctx, up.BlobID, "recipe.txt", "text/plain")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(body)
		body.Close()
		if string(got) != recipe {
			t.Errorf("downloaded %q", got)
		}
	}

	_, err := c.Download(ctx, "no-such-blob", "x", "text/plain")
	checkRefused(t, "a blob that isn't there", err, 404, "")
}
