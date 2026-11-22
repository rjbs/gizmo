package httpd_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rjbs/gizmo/internal/bakery"
	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/jmaptest"
	"github.com/rjbs/gizmo/push"
)

// env is a running bakery and a tester for each way of calling it.
type env struct {
	t      *testing.T
	bakery *bakery.Bakery

	// alice has no token, and so is alice by the loopback credential;
	// her account is the kitchen.  cellar is alice by the token that sees
	// only the cellar, and bob is bob.  stranger has a token nobody holds.
	alice, cellar, bob, stranger *jmaptest.Tester
}

func newEnv(t *testing.T, limits jmap.Limits) *env {
	t.Helper()
	b := bakery.New(limits)
	srv := httptest.NewServer(b.Handler)
	t.Cleanup(srv.Close)

	alice := jmaptest.New(t, srv.URL, jmap.CoreCapability, bakery.Capability)
	alice.AccountID = "kitchen"
	as := func(token, account string) *jmaptest.Tester {
		return alice.With(func(c *jmaptest.Tester) { c.Authorization, c.AccountID = "Bearer "+token, account })
	}
	return &env{
		t: t, bakery: b, alice: alice,
		cellar:   as(b.CellarToken, "cellar"),
		bob:      as(b.BobToken, "bobs"),
		stranger: as(strings.Replace(b.BobToken, "bakery-", "bakery-x", 1)[:len(b.BobToken)], "bobs"),
	}
}

// checkJSON compares a value with the JSON expected of it.
func checkJSON(t *testing.T, name string, got any, want string) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var x, y any
	if err := json.Unmarshal(g, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &y); err != nil {
		t.Fatalf("%s: bad expected JSON %s: %v", name, want, err)
	}
	if !reflect.DeepEqual(x, y) {
		t.Errorf("%s:\n got %s\nwant %s", name, g, want)
	}
}

// checkSession checks who a tester's session says it is, which accounts it
// lists, as "id=name" in order, and its primary account for the bakery.
func checkSession(t *testing.T, name string, c *jmaptest.Tester, wantUser, wantAccounts, wantPrimary string) {
	t.Helper()
	s := c.Session()
	accounts := s["accounts"].(map[string]any)
	var ids []string
	for _, id := range []string{"attic", "bobs", "cellar", "kitchen"} {
		if a, ok := accounts[id].(map[string]any); ok {
			ids = append(ids, id+"="+a["name"].(string))
		}
	}
	primary, _ := s["primaryAccounts"].(map[string]any)[bakery.Capability].(string)
	if s["username"] != wantUser || strings.Join(ids, " ") != wantAccounts || primary != wantPrimary {
		t.Errorf("%s: got user %v, accounts %q, primary %q; want %q, %q, %q",
			name, s["username"], strings.Join(ids, " "), primary, wantUser, wantAccounts, wantPrimary)
	}
}

func TestSession(t *testing.T) {
	e := newEnv(t, jmap.Limits{MaxSizeUpload: 5000, MaxObjectsInGet: 17})

	checkSession(t, "by loopback", e.alice, "alice", "cellar=cellar kitchen=The Kitchen", "kitchen")
	checkSession(t, "by a token for one account", e.cellar, "alice", "cellar=cellar", "")
	checkSession(t, "another user", e.bob, "bob", "bobs=bobs", "bobs")

	s := e.alice.Session()
	base := e.alice.BaseURL
	checkJSON(t, "the core capability", s["capabilities"].(map[string]any)[jmap.CoreCapability], `{
		"maxSizeUpload": 5000, "maxConcurrentUpload": 4, "maxSizeRequest": 10000000,
		"maxConcurrentRequests": 4, "maxCallsInRequest": 64, "maxObjectsInGet": 17,
		"maxObjectsInSet": 1000, "collationAlgorithms": []}`)
	checkJSON(t, "the server's own capability", s["capabilities"].(map[string]any)[bakery.Capability], `{}`)
	checkJSON(t, "an account", s["accounts"].(map[string]any)["kitchen"], `{
		"name": "The Kitchen", "isPersonal": true, "isReadOnly": false,
		"accountCapabilities": {"`+bakery.Capability+`": {"oven": "kitchen"}}}`)
	checkJSON(t, "the URLs",
		[]any{s["apiUrl"], s["downloadUrl"], s["uploadUrl"], s["eventSourceUrl"]},
		`["`+base+`/jmap/api", "`+base+`/jmap/download/{accountId}/{blobId}/{name}?type={type}",
		  "`+base+`/jmap/upload/{accountId}/", "`+base+`/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}"]`)

	if a, c := s["state"], e.cellar.Session()["state"]; a == "" || a == c {
		t.Errorf("sessions that differ should have states that differ, but got %q and %q", a, c)
	}
	if got := e.alice.Request(jmaptest.Do("Core/echo", nil)).SessionState; got != s["state"] {
		t.Errorf("a response's sessionState is %q, but the session's state is %q", got, s["state"])
	}
	if raw := e.stranger.Post(`{"using":[],"methodCalls":[]}`); raw.Status != 401 {
		t.Errorf("a token nobody holds got status %d, not 401", raw.Status)
	}
}

func TestAPI(t *testing.T) {
	e := newEnv(t, jmap.Limits{MaxCallsInRequest: 3, MaxSizeRequest: 2000})

	set := e.alice.Request(jmaptest.Do("Cookie/set", jmaptest.Args{
		"create": jmaptest.Args{"a": jmaptest.Args{"flavor": "ginger"}, "b": jmaptest.Args{"flavor": ""}},
	})).SingleSentence("Cookie/set").AsSet()
	if id := set.CreatedID("a"); id != "001" {
		t.Errorf("the first cookie's id is %q, not the string 001", id)
	}
	checkJSON(t, "the one that failed", set.NotCreated("b"),
		`{"type": "invalidProperties", "description": "a cookie needs a flavor", "properties": ["flavor"]}`)

	r := e.alice.Request(
		jmaptest.Do("Cookie/set", jmaptest.Args{"create": jmaptest.Args{"k": jmaptest.Args{"flavor": "oat"}}}),
		jmaptest.Do("Cookie/get", jmaptest.Args{"ids": []string{"#k"}}),
		jmaptest.Do("Cookie/get", jmaptest.Args{"#ids": jmaptest.Ref("1", "Cookie/get", "/list/*/id")}),
	)
	r.Sentence(0).AsSet().AssertNoErrors()
	checkJSON(t, "a cookie got by its creation id", r.Sentence(1).Args["list"], `[{"id": "002", "flavor": "oat"}]`)
	checkJSON(t, "a cookie got by a result reference", r.Sentence(2).Args["list"], `[{"id": "002", "flavor": "oat"}]`)

	e.alice.Request(jmaptest.Do("Cookie/get", jmaptest.Args{"accountId": "cellar"})).SingleSentence("Cookie/get")
	e.alice.Request(jmaptest.Do("Cookie/get", jmaptest.Args{"accountId": "bobs"})).Sentence(0).AssertError("accountNotFound")
	e.alice.Request(jmaptest.Do("Cookie/get", jmaptest.Args{"accountId": "nowhere"})).Sentence(0).AssertError("accountNotFound")
	e.alice.Request(jmaptest.Do("Cookie/get", jmaptest.Args{"accountId": "attic"})).Sentence(0).AssertError("accountNotFound")
	e.alice.Request(jmaptest.Do("Cookie/get", jmaptest.Args{"accountId": jmaptest.Omit})).Sentence(0).AssertError("invalidArguments")
	e.cellar.Request(jmaptest.Do("Cookie/get", jmaptest.Args{"accountId": "kitchen"})).Sentence(0).AssertError("accountNotFound")

	e.alice.Post(`{"using":`).AssertRequestError(t, "notJSON")
	e.alice.Post(`{"using":[]}`).AssertRequestError(t, "notRequest")
	e.alice.Post(`{"using":["urn:nope"],"methodCalls":[]}`).AssertRequestError(t, "unknownCapability")
	echo := `["Core/echo",{},"a"]`
	problem := e.alice.Post(`{"using":[],"methodCalls":[`+strings.Join([]string{echo, echo, echo, echo}, ",")+`]}`).AssertRequestError(t, "limit")
	checkJSON(t, "too many calls", problem["limit"], `"maxCallsInRequest"`)
	problem = e.alice.Post(`{"using":[],"methodCalls":[["Core/echo",{"pad":"`+strings.Repeat("x", 2000)+`"},"a"]]}`).AssertRequestError(t, "limit")
	checkJSON(t, "too large a request", problem["limit"], `"maxSizeRequest"`)
}

func TestBlobs(t *testing.T) {
	e := newEnv(t, jmap.Limits{})

	up := e.alice.Upload("text/plain", []byte("flour, sugar, butter"))
	if up.Status != 201 || up.Size != 20 || up.Type != "text/plain" {
		t.Fatalf("upload: got status %d, size %d, type %q: %s", up.Status, up.Size, up.Type, up.Body)
	}
	down := e.alice.Download(up.BlobID, "recipe.txt", "")
	if down.Status != 200 || string(down.Body) != "flour, sugar, butter" {
		t.Errorf("download: got status %d and %q", down.Status, down.Body)
	}

	inAccount := func(c *jmaptest.Tester, id string) *jmaptest.Tester {
		return c.With(func(c *jmaptest.Tester) { c.AccountID = id })
	}
	// checkNoBlobs checks that a tester can neither download the blob
	// from the kitchen nor upload there, and is told only "not found".
	checkNoBlobs := func(name string, c *jmaptest.Tester) {
		t.Helper()
		if got := c.Download(up.BlobID, "recipe.txt", "").Status; got != 404 {
			t.Errorf("%s: download got status %d, not 404", name, got)
		}
		if got := c.Upload("text/plain", []byte("salt")).Status; got != 404 {
			t.Errorf("%s: upload got status %d, not 404", name, got)
		}
	}
	checkNoBlobs("another user", inAccount(e.bob, "kitchen"))
	checkNoBlobs("a token limited to another account", inAccount(e.cellar, "kitchen"))
	checkNoBlobs("an account with no data", inAccount(e.alice, "attic"))
	checkNoBlobs("an account that doesn't exist", inAccount(e.alice, "nowhere"))
}

func TestEventSource(t *testing.T) {
	e := newEnv(t, jmap.Limits{})
	kitchen, cellar := e.bakery.Account("kitchen"), e.bakery.Account("cellar")
	kitchen.Bake("ginger")

	all := e.alice.Listen("types=*&closeafter=no&ping=0")
	checkJSON(t, "the states on connecting", all.NextState(), `{"kitchen": {"Cookie": "1"}, "cellar": {"Cookie": "0"}}`)
	all.AssertQuiet()

	e.alice.Request(jmaptest.Do("Cookie/set", jmaptest.Args{"create": jmaptest.Args{"a": jmaptest.Args{"flavor": "oat"}}}))
	checkJSON(t, "a change made by a request", all.NextState(), `{"kitchen": {"Cookie": "2"}}`)
	cellar.Bake("rye")
	checkJSON(t, "a change made outside any request", all.NextState(), `{"cellar": {"Cookie": "1"}}`)
	e.bakery.Account("bobs").Bake("plain")
	all.AssertQuiet()

	limited := e.cellar.Listen("types=*&closeafter=no&ping=0")
	checkJSON(t, "a token sees only its accounts", limited.NextState(), `{"cellar": {"Cookie": "1"}}`)
	kitchen.Bake("fig")
	limited.AssertQuiet()
	checkJSON(t, "while the other sees it", all.NextState(), `{"kitchen": {"Cookie": "3"}}`)

	none := e.alice.Listen("types=Biscuit&closeafter=no&ping=0")
	none.AssertQuiet()
	kitchen.Bake("lemon")
	none.AssertQuiet()

	once := e.alice.Listen("types=Cookie&closeafter=state&ping=0")
	once.AssertQuiet()
	kitchen.Bake("date")
	checkJSON(t, "closeafter=state waits for a change", once.NextState(), `{"kitchen": {"Cookie": "5"}}`)
	once.AssertClosed()

	pinged := e.alice.Listen("types=Biscuit&closeafter=no&ping=1")
	if ev := pinged.Next(); ev.Type != "ping" || ev.Data != `{"interval":1}` {
		t.Errorf("expected a ping, but got %q: %s", ev.Type, ev.Data)
	}

	for _, query := range []string{"closeafter=sometime", "ping=soon", "ping=-1"} {
		if got := e.alice.Listen(query).Status; got != 400 {
			t.Errorf("%s: got status %d, not 400", query, got)
		}
	}
	if got := e.stranger.Listen("types=*").Status; got != 401 {
		t.Errorf("a token nobody holds got status %d, not 401", got)
	}
}

func TestWatch(t *testing.T) {
	e := newEnv(t, jmap.Limits{})
	kitchen := e.bakery.Account("kitchen")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go push.Watch(ctx, e.bakery.Broker, "kitchen", 5*time.Millisecond, kitchen.States)

	stream := e.alice.Listen("types=*&closeafter=no&ping=0")
	stream.NextState()
	kitchen.Sneak("ginger")
	checkJSON(t, "a change nothing published", stream.NextState(), `{"kitchen": {"Cookie": "1"}}`)
	kitchen.Bake("oat")
	checkJSON(t, "a change that was published is heard of once", stream.NextState(), `{"kitchen": {"Cookie": "2"}}`)
	stream.AssertQuiet()
}
