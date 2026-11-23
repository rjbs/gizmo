package std_test

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rjbs/gizmo/internal/bakery"
	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/jmaptest"
)

type args = jmaptest.Args

// kitchen is a running bakery and alice's tester for her kitchen.
type kitchen struct {
	*jmaptest.Tester
	t      *testing.T
	bakery *bakery.Bakery
}

// newKitchen starts a bakery.  Its /query gives at most four ids, whatever
// the limits given.
func newKitchen(t *testing.T, limits jmap.Limits) *kitchen {
	t.Helper()
	b := bakery.New(limits)
	srv := httptest.NewServer(b.Handler)
	t.Cleanup(srv.Close)
	c := jmaptest.New(t, srv.URL, jmap.CoreCapability, bakery.Capability)
	c.AccountID = "kitchen"
	return &kitchen{Tester: c, t: t, bakery: b}
}

// bake makes a cookie of each flavor with Cookie/set and returns their ids,
// which are "001", "002" and so on in a new kitchen.
func (k *kitchen) bake(flavors ...string) []string {
	k.t.Helper()
	create := args{}
	for i, f := range flavors {
		create[string(rune('a'+i))] = args{"flavor": f}
	}
	set := k.Request(jmaptest.Do("Cookie/set", args{"create": create})).SingleSentence("Cookie/set").AsSet().AssertNoErrors()
	ids := make([]string, len(flavors))
	for i := range flavors {
		ids[i] = set.CreatedID(string(rune('a' + i)))
	}
	return ids
}

// call makes one method call and returns its one sentence.
func (k *kitchen) call(name string, a args) *jmaptest.Sentence {
	k.t.Helper()
	return k.Request(jmaptest.Do(name, a)).AssertSentences(1)[0]
}

// state returns a type's current state.
func (k *kitchen) state(typ string) string {
	k.t.Helper()
	return k.call(typ+"/get", args{"ids": []string{}}).Str("state")
}

// flavors gives the flavor of each cookie in a /get response's list, in
// order, joined by spaces.
func flavors(s *jmaptest.Sentence) string {
	var got struct {
		List []struct {
			Flavor string `json:"flavor"`
		} `json:"list"`
	}
	s.Decode(&got)
	out := make([]string, len(got.List))
	for i, c := range got.List {
		out[i] = c.Flavor
	}
	return strings.Join(out, " ")
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

func checkEqual(t *testing.T, name string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %#v\nwant %#v", name, got, want)
	}
}

// ---- /get -------------------------------------------------------------------

// checkGet checks a Cookie/get: the flavors listed, in order, and the ids
// not found.
func checkGet(t *testing.T, k *kitchen, name string, a args, wantFlavors string, wantNotFound ...string) {
	t.Helper()
	s := k.call("Cookie/get", a).AssertNamed("Cookie/get")
	checkEqual(t, name+": flavors", flavors(s), wantFlavors)
	checkEqual(t, name+": notFound", s.Strs("notFound"), append([]string{}, wantNotFound...))
}

// checkGetError checks that a Cookie/get fails with the given method error.
func checkGetError(t *testing.T, k *kitchen, name string, a args, want string) {
	t.Helper()
	if s := k.call("Cookie/get", a); s.Name != "error" || s.Args["type"] != want {
		t.Errorf("%s: got %s, want the error %s", name, s, want)
	}
}

func TestGet(t *testing.T) {
	k := newKitchen(t, jmap.Limits{MaxObjectsInGet: 4})
	k.bake("ginger", "oat", "rye")

	checkGet(t, k, "everything", args{}, "ginger oat rye")
	checkGet(t, k, "everything, asked for as null", args{"ids": nil}, "ginger oat rye")
	checkGet(t, k, "nothing", args{"ids": []string{}}, "")
	checkGet(t, k, "by id, in the order asked", args{"ids": []string{"003", "001"}}, "rye ginger")
	checkGet(t, k, "an id asked for twice", args{"ids": []string{"002", "002"}}, "oat")
	checkGet(t, k, "ids that name nothing", args{"ids": []string{"900", "002", "3"}}, "oat", "900", "3")
	checkGet(t, k, "a creation reference nothing made", args{"ids": []string{"#x", "001"}}, "ginger", "#x")
	checkGet(t, k, "an argument of the type's own", args{"ids": []string{"001"}, "shout": true}, "GINGER")

	one := k.call("Cookie/get", args{"ids": []string{"001"}, "properties": []string{"flavor"}})
	checkJSON(t, "only the properties asked for, and the id", one.Args["list"], `[{"id": "001", "flavor": "ginger"}]`)
	one = k.call("Cookie/get", args{"ids": []string{"001"}, "properties": []string{}})
	checkJSON(t, "no properties asked for leaves the id", one.Args["list"], `[{"id": "001"}]`)
	if !strings.Contains(string(one.Raw), `"id":"001"`) {
		t.Errorf("an id of digits should be sent as a string: %s", one.Raw)
	}
	checkEqual(t, "the state", one.Str("state"), "3")
	checkEqual(t, "the account", one.Str("accountId"), "kitchen")

	checkGetError(t, k, "a property the type hasn't got", args{"properties": []string{"crunch"}}, "invalidArguments")
	checkGetError(t, k, "an argument nobody knows", args{"loud": true}, "invalidArguments")
	checkGetError(t, k, "ids of the wrong type", args{"ids": "001"}, "invalidArguments")
	checkGetError(t, k, "too many ids", args{"ids": []string{"1", "2", "3", "4", "5"}}, "requestTooLarge")

	checkGet(t, k, "as many ids as are allowed", args{"ids": []string{"1", "2", "3", "001"}}, "ginger", "1", "2", "3")
	k.bake("fig")
	checkGet(t, k, "as many objects as are allowed", args{}, "ginger oat rye fig")
	k.bake("date")
	checkGetError(t, k, "everything, when that is too many", args{}, "requestTooLarge")
	// Jar has no Count, so its objects are counted once loaded.
	for _, name := range []string{"a", "b", "c", "d"} {
		k.call("Jar/set", args{"create": args{"j": args{"name": name}}}).AsSet().AssertNoErrors()
	}
	k.call("Jar/get", args{}).AssertNamed("Jar/get")
	k.call("Jar/set", args{"create": args{"j": args{"name": "e"}}}).AsSet().AssertNoErrors()
	k.call("Jar/get", args{}).AssertError("requestTooLarge")
}

// ---- /set -------------------------------------------------------------------

func TestSetBasics(t *testing.T) {
	k := newKitchen(t, jmap.Limits{})

	set := k.call("Cookie/set", args{"create": args{"g": args{"flavor": "ginger"}, "o": args{"flavor": "oat", "notes": args{"crisp": true}}}}).AsSet()
	checkEqual(t, "states of a create", []string{set.OldState(), set.NewState()}, []string{"0", "2"})
	g := set.Created("g")
	checkJSON(t, "what the client is told of a creation: the id and what it didn't send",
		args{"id": g["id"], "bakedBy": g["bakedBy"], "notes": g["notes"]}, `{"id": "001", "bakedBy": "alice", "notes": {}}`)
	if _, told := set.Created("o")["notes"]; told {
		t.Errorf("the client was told of notes it sent itself: %s", set)
	}
	for _, nothing := range []string{"updated", "destroyed", "notCreated", "notUpdated", "notDestroyed"} {
		if v, present := set.Args[nothing]; !present || v != nil {
			t.Errorf("%s should be null when there is nothing to say, but: %s", nothing, set)
		}
	}

	set = k.call("Cookie/set", args{"update": args{"001": args{"flavor": "ginger snap", "notes/hot": true}}}).AsSet().AssertNoErrors()
	checkJSON(t, "an update with nothing to tell", set.Args["updated"], `{"001": null}`)
	got := k.call("Cookie/get", args{"ids": []string{"001"}, "properties": []string{"flavor", "notes"}})
	checkJSON(t, "the updated object", got.Args["list"], `[{"id": "001", "flavor": "ginger snap", "notes": {"hot": true}}]`)

	set = k.call("Cookie/set", args{"destroy": []string{"002", "002"}}).AsSet().AssertNoErrors()
	checkEqual(t, "destroyed, once", set.DestroyedIDs(), []string{"002"})
	checkGet(t, k, "what is left", args{}, "ginger snap")
	checkEqual(t, "states of a destroy", []string{set.OldState(), set.NewState()}, []string{"3", "4"})
}

// checkSetError makes a Cookie/set expected to fail for one object, and
// checks the type of the SetError found under key in the given list:
// notCreated, notUpdated or notDestroyed.
func checkSetError(t *testing.T, k *kitchen, name string, a args, list, key, want string) {
	t.Helper()
	set := k.call("Cookie/set", a).AsSet()
	errs, _ := set.Args[list].(map[string]any)
	e, _ := errs[key].(map[string]any)
	if e["type"] != want {
		t.Errorf("%s: expected %s for %q in %s, but got %s", name, want, key, list, set)
	}
}

func TestSetErrors(t *testing.T) {
	k := newKitchen(t, jmap.Limits{MaxObjectsInSet: 4})
	k.bake("ginger", "oat")
	before := k.state("Cookie")

	checkSetError(t, k, "a bad property", args{"create": args{"x": args{"flavor": ""}}}, "notCreated", "x", "invalidProperties")
	checkSetError(t, k, "an unknown property", args{"create": args{"x": args{"flavor": "fig", "crunch": 3}}}, "notCreated", "x", "invalidProperties")
	checkSetError(t, k, "a server-set property", args{"create": args{"x": args{"flavor": "fig", "bakedBy": "eve"}}}, "notCreated", "x", "invalidProperties")
	checkSetError(t, k, "a creation that isn't an object", args{"create": args{"x": "fig"}}, "notCreated", "x", "invalidProperties")
	checkSetError(t, k, "a flavor already taken", args{"create": args{"x": args{"flavor": "oat"}}}, "notCreated", "x", "alreadyExists")
	checkSetError(t, k, "an update of nothing", args{"update": args{"900": args{"flavor": "fig"}}}, "notUpdated", "900", "notFound")
	checkSetError(t, k, "an update of a creation never made", args{"update": args{"#x": args{"flavor": "fig"}}}, "notUpdated", "#x", "notFound")
	checkSetError(t, k, "a patch that isn't an object", args{"update": args{"001": 7}}, "notUpdated", "001", "invalidProperties")
	checkSetError(t, k, "a patch through nothing", args{"update": args{"001": args{"notes/a/b": 1}}}, "notUpdated", "001", "invalidPatch")
	checkSetError(t, k, "a patch of a server-set property", args{"update": args{"001": args{"bakedBy": "eve"}}}, "notUpdated", "001", "invalidProperties")
	checkSetError(t, k, "a destroy of nothing", args{"destroy": []string{"900"}}, "notDestroyed", "900", "notFound")
	checkSetError(t, k, "a destroy of a creation never made", args{"destroy": []string{"#x"}}, "notDestroyed", "#x", "notFound")
	checkEqual(t, "the state after nothing but failures", k.state("Cookie"), before)

	k.call("Jar/set", args{"create": args{"j": args{"name": "tin"}}}).AsSet().AssertNoErrors()
	jar := k.call("Jar/set", args{"destroy": []string{"003"}}).AsSet()
	checkJSON(t, "what a type has no function for is forbidden", jar.NotDestroyed("003"), `{"type": "forbidden"}`)

	k.call("Cookie/set", args{"ifInState": "nope", "create": args{"x": args{"flavor": "fig"}}}).AssertError("stateMismatch")
	checkEqual(t, "nothing was done in the wrong state", k.state("Cookie"), before)
	k.call("Cookie/set", args{"ifInState": before, "create": args{"x": args{"flavor": "fig"}}}).AsSet().AssertNoErrors()

	k.call("Cookie/set", args{"wrap": true}).AssertError("invalidArguments")
	k.call("Cookie/set", args{"destroy": "001"}).AssertError("invalidArguments")
	k.call("Cookie/set", args{
		"create": args{"a": args{"flavor": "a"}, "b": args{"flavor": "b"}}, "update": args{"001": args{}}, "destroy": []string{"001", "002"},
	}).AssertError("requestTooLarge")
}

// Each object stands or falls alone.
func TestSetIsPerObject(t *testing.T) {
	k := newKitchen(t, jmap.Limits{})
	k.bake("ginger", "oat", "rye")

	set := k.call("Cookie/set", args{
		"create":  args{"good": args{"flavor": "fig"}, "bad": args{"flavor": "ginger"}},
		"update":  args{"001": args{"notes/x": 1}, "002": args{"flavor": "ginger"}},
		"destroy": []string{"003", "900"},
	}).AsSet()
	set.CreatedID("good")
	checkJSON(t, "the creation that failed", set.NotCreated("bad")["existingId"], `"001"`)
	checkEqual(t, "the update that worked", set.UpdatedIDs(), []string{"001"})
	checkJSON(t, "the update that failed", set.NotUpdated("002")["type"], `"alreadyExists"`)
	checkEqual(t, "the destroy that worked", set.DestroyedIDs(), []string{"003"})
	set.NotDestroyed("900")
	checkGet(t, k, "what all that left", args{}, "ginger oat fig")
}

// What is destroyed in a call is gone before anything in it is created or
// updated, and what is created is there before anything is updated.
func TestSetOrder(t *testing.T) {
	k := newKitchen(t, jmap.Limits{})
	k.bake("ginger", "oat")

	set := k.call("Cookie/set", args{
		"destroy": []string{"001"},
		"create":  args{"new": args{"flavor": "ginger"}},
		"update":  args{"002": args{"flavor": "oat bran"}, "#new": args{"notes/fresh": true}},
	}).AsSet().AssertNoErrors()
	id := set.CreatedID("new")
	checkEqual(t, "updated, by their own ids", set.UpdatedIDs(), []string{"002", id})
	got := k.call("Cookie/get", args{"ids": []string{id}, "properties": []string{"flavor", "notes"}})
	checkJSON(t, "the object created and then updated", got.Args["list"], `[{"id": "003", "flavor": "ginger", "notes": {"fresh": true}}]`)

	set = k.call("Cookie/set", args{"create": args{"brief": args{"flavor": "mayfly"}}, "destroy": []string{"#brief"}}).AsSet().AssertNoErrors()
	checkEqual(t, "an object made and destroyed in one call", set.DestroyedIDs(), []string{set.CreatedID("brief")})
	checkGet(t, k, "and it is gone", args{}, "oat bran ginger")
}

// One object's change may change others, of other types, and is undone
// with them if it fails.
func TestSetSideEffects(t *testing.T) {
	k := newKitchen(t, jmap.Limits{})
	ids := k.bake("ginger", "oat", "rye")
	k.call("Cookie/set", args{"update": args{"002": args{"notes/cursed": true}}}).AsSet().AssertNoErrors()
	jar := k.call("Jar/set", args{"create": args{"j": args{"name": "tin", "cookieIds": ids}}}).AsSet().AssertNoErrors().CreatedID("j")
	jarState := k.state("Jar")
	stream := k.Listen("types=*&closeafter=no&ping=0")
	stream.NextState()

	contents := func() any {
		t.Helper()
		return k.call("Jar/get", args{"ids": []string{jar}, "properties": []string{"cookieIds"}}).Args["list"].([]any)[0].(map[string]any)["cookieIds"]
	}

	set := k.call("Cookie/set", args{"destroy": []string{"002"}}).AsSet()
	checkJSON(t, "a destroy that failed part way", set.NotDestroyed("002")["type"], `"cursed"`)
	checkJSON(t, "left the jar as it was", contents(), `["001", "002", "003"]`)
	checkEqual(t, "and the jar's state as it was", k.state("Jar"), jarState)
	stream.AssertQuiet()

	k.call("Cookie/set", args{"destroy": []string{"001", "002"}}).AsSet()
	checkJSON(t, "a destroy that worked took its cookie out of the jar, and the other didn't", contents(), `["002", "003"]`)
	if k.state("Jar") == jarState {
		t.Errorf("the jar changed, but its state did not")
	}
	changed := stream.NextState()["kitchen"]
	if changed["Cookie"] == "" || changed["Jar"] == "" || changed["Jar"] != k.state("Jar") {
		t.Errorf("a change to both types should be announced for both, but: %v", changed)
	}
}

// Whoever writes the type's functions can find out who is calling, and in
// which request.
func TestSetKnowsTheCaller(t *testing.T) {
	k := newKitchen(t, jmap.Limits{})
	batch := func(s *jmaptest.Sentence, cid string) string {
		t.Helper()
		b, _ := s.AsSet().Created(cid)["batch"].(string)
		if b == "" {
			t.Fatalf("no batch for %s: %s", cid, s)
		}
		return b
	}
	r := k.Request(
		jmaptest.Do("Cookie/set", args{"create": args{"a": args{"flavor": "a"}, "b": args{"flavor": "b"}}}),
		jmaptest.Do("Cookie/set", args{"create": args{"c": args{"flavor": "c"}}}),
	)
	first := batch(r.Sentence(0), "a")
	checkEqual(t, "two objects of one call", batch(r.Sentence(0), "b"), first)
	checkEqual(t, "two calls of one request", batch(r.Sentence(1), "c"), first)
	if other := batch(k.call("Cookie/set", args{"create": args{"d": args{"flavor": "d"}}}), "d"); other == first {
		t.Errorf("two requests had the same id, %s", first)
	}

	bob := k.With(func(c *jmaptest.Tester) { c.Authorization, c.AccountID = "Bearer "+k.bakery.BobToken, "bobs" })
	made := bob.Request(jmaptest.Do("Cookie/set", args{"create": args{"x": args{"flavor": "x"}}})).Sentence(0).AsSet()
	checkJSON(t, "another caller", made.Created("x")["bakedBy"], `"bob"`)
}

// ---- /changes ---------------------------------------------------------------

// checkChanges checks a Cookie/changes since a state: the ids created,
// updated and destroyed, each joined by spaces, the new state, and whether
// there is more.
func checkChanges(t *testing.T, k *kitchen, name, since string, maxChanges int, wantCreated, wantUpdated, wantDestroyed, wantState string, wantMore bool) {
	t.Helper()
	a := args{"sinceState": since}
	if maxChanges > 0 {
		a["maxChanges"] = maxChanges
	}
	s := k.call("Cookie/changes", a).AssertNamed("Cookie/changes")
	got := []any{strings.Join(s.Strs("created"), " "), strings.Join(s.Strs("updated"), " "), strings.Join(s.Strs("destroyed"), " "),
		s.Str("newState"), s.Args["hasMoreChanges"], s.Str("oldState")}
	want := []any{wantCreated, wantUpdated, wantDestroyed, wantState, wantMore, since}
	checkEqual(t, name, got, want)
}

func TestChanges(t *testing.T) {
	k := newKitchen(t, jmap.Limits{})
	k.bake("ginger", "oat", "rye") // states 1, 2, 3
	checkChanges(t, k, "no changes", "3", 0, "", "", "", "3", false)
	checkChanges(t, k, "everything is new", "0", 0, "001 002 003", "", "", "3", false)

	k.call("Cookie/set", args{"update": args{"001": args{"notes/a": 1}}}) // 4
	k.call("Cookie/set", args{"destroy": []string{"002"}})                // 5
	k.bake("fig")                                                         // 6: 004
	k.call("Cookie/set", args{"destroy": []string{"004"}})                // 7
	checkChanges(t, k, "an update, a destroy, and a cookie that came and went", "3", 0, "", "001", "002", "7", false)
	checkChanges(t, k, "what was made and unmade since is not mentioned", "0", 0, "003 001", "", "", "7", false)
	checkChanges(t, k, "what the client saw made is destroyed", "6", 0, "", "", "004", "7", false)

	checkChanges(t, k, "the first page", "3", 1, "", "001", "", "4", true)
	checkChanges(t, k, "the next page", "4", 1, "", "", "002", "5", true)
	checkChanges(t, k, "a page that ends where the changes do", "4", 3, "", "", "002", "7", false)

	k.call("Cookie/changes", args{"sinceState": "8"}).AssertError("cannotCalculateChanges")
	k.call("Cookie/changes", args{"sinceState": "yesterday"}).AssertError("cannotCalculateChanges")
	k.call("Cookie/changes", args{}).AssertError("invalidArguments")
	k.call("Cookie/changes", args{"sinceState": "3", "maxChanges": 0}).AssertError("invalidArguments")
	k.call("Cookie/changes", args{"sinceState": "3", "since": "then"}).AssertError("invalidArguments")
}

// ---- /query -----------------------------------------------------------------

// checkQuery checks a Cookie/query: the flavors of the ids it returns, in
// order, joined by spaces, and the position it reports.  The cookies are
// those of TestQuery, whose flavor and id are told apart by flavorOf.
func checkQuery(t *testing.T, k *kitchen, name string, a args, wantFlavors string, wantPosition int) *jmaptest.Sentence {
	t.Helper()
	s := k.call("Cookie/query", a).AssertNamed("Cookie/query")
	got := make([]string, 0)
	for _, id := range s.Strs("ids") {
		got = append(got, queryFlavors[id])
	}
	position, _ := s.Args["position"].(json.Number).Int64()
	checkEqual(t, name, []any{strings.Join(got, " "), int(position)}, []any{wantFlavors, wantPosition})
	return s
}

var queryFlavors = map[string]string{"001": "fig", "002": "date", "003": "apple", "004": "elder", "005": "cherry", "006": "banana"}

func TestQuery(t *testing.T) {
	k := newKitchen(t, jmap.Limits{})
	k.bake("fig", "date", "apple", "elder", "cherry", "banana")
	byFlavor := []args{{"property": "flavor"}}
	down := []args{{"property": "flavor", "isAscending": false}}
	// A filter that matches everything sends the query the long way
	// round, by way of every match; no filter lets the type page.
	everything := args{"flavorContains": ""}

	for _, way := range []struct {
		name   string
		filter any
	}{{"paged by the type", nil}, {"paged by the driver", everything}} {
		q := func(a args) args {
			if way.filter != nil {
				a["filter"] = way.filter
			}
			return a
		}
		s := checkQuery(t, k, way.name+": by id, by default", q(args{}), "fig date apple elder", 0)
		checkJSON(t, way.name+": the limit the server imposed", s.Args["limit"], `4`)
		checkEqual(t, way.name+": the query state", s.Str("queryState"), k.state("Cookie"))
		checkJSON(t, way.name+": no query changes", s.Args["canCalculateChanges"], `false`)
		if _, present := s.Args["total"]; present {
			t.Errorf("%s: a total nobody asked for: %s", way.name, s)
		}

		checkQuery(t, k, way.name+": sorted", q(args{"sort": byFlavor}), "apple banana cherry date", 0)
		checkQuery(t, k, way.name+": sorted the other way", q(args{"sort": down}), "fig elder date cherry", 0)
		s = checkQuery(t, k, way.name+": a position and a limit", q(args{"sort": byFlavor, "position": 4, "limit": 1, "calculateTotal": true}), "elder", 4)
		checkJSON(t, way.name+": the total", s.Args["total"], `6`)
		if _, present := s.Args["limit"]; present {
			t.Errorf("%s: a limit reported that was the client's own: %s", way.name, s)
		}
		checkQuery(t, k, way.name+": past the end", q(args{"sort": byFlavor, "position": 9}), "", 6)
		checkQuery(t, k, way.name+": a limit of none", q(args{"sort": byFlavor, "limit": 0}), "", 0)
		s = checkQuery(t, k, way.name+": the last of them", q(args{"sort": byFlavor, "position": 4}), "elder fig", 4)
		if _, present := s.Args["limit"]; present {
			t.Errorf("%s: a limit reported that didn't cut anything off: %s", way.name, s)
		}
	}

	checkQuery(t, k, "a filter", args{"filter": args{"flavorContains": "an"}, "sort": byFlavor}, "banana", 0)
	checkQuery(t, k, "a filter that matches nothing", args{"filter": args{"flavorContains": "zz"}}, "", 0)
	checkQuery(t, k, "a filter of null is no filter", args{"filter": nil, "sort": byFlavor, "limit": 2}, "apple banana", 0)
	checkQuery(t, k, "from the end", args{"sort": byFlavor, "position": -2}, "elder fig", 4)
	checkQuery(t, k, "from before the start", args{"sort": byFlavor, "position": -20, "limit": 2}, "apple banana", 0)
	checkQuery(t, k, "from an anchor", args{"sort": byFlavor, "anchor": "005", "limit": 2}, "cherry date", 2)
	checkQuery(t, k, "from after an anchor", args{"sort": byFlavor, "anchor": "005", "anchorOffset": 2, "limit": 2}, "elder fig", 4)
	checkQuery(t, k, "from before an anchor", args{"sort": byFlavor, "anchor": "005", "anchorOffset": -1, "limit": 2}, "banana cherry", 1)
	checkQuery(t, k, "from far before an anchor", args{"sort": byFlavor, "anchor": "005", "anchorOffset": -9, "limit": 2}, "apple banana", 0)

	k.call("Cookie/query", args{"anchor": "900"}).AssertError("anchorNotFound")
	k.call("Cookie/query", args{"filter": args{"flavorContains": "an"}, "anchor": "001"}).AssertError("anchorNotFound")
	k.call("Cookie/query", args{"limit": -1}).AssertError("invalidArguments")
	k.call("Cookie/query", args{"sort": []args{{"property": "crunch"}}}).AssertError("unsupportedSort")
	k.call("Cookie/query", args{"sort": []args{{"property": "flavor", "seed": 4}}}).AssertError("invalidArguments")
	k.call("Cookie/query", args{"filter": args{"shape": "round"}}).AssertError("unsupportedFilter")
	k.call("Cookie/query", args{"collapse": true}).AssertError("invalidArguments")

	r := k.Request(
		jmaptest.Do("Cookie/set", args{"create": args{"new": args{"flavor": "cranberry"}}}),
		jmaptest.Do("Cookie/query", args{"sort": byFlavor, "anchor": "#new", "limit": 1}),
	)
	checkEqual(t, "an anchor that is a creation reference", r.Sentence(1).Strs("ids"), []string{r.Sentence(0).AsSet().CreatedID("new")})
}

// ---- a method of the application's own --------------------------------------

func TestCustomMethod(t *testing.T) {
	k := newKitchen(t, jmap.Limits{})
	k.bake("ginger")
	stream := k.Listen("types=Cookie&closeafter=no&ping=0")
	stream.NextState()

	r := k.Request(
		jmaptest.Do("Cookie/import", args{"flavors": args{"a": "oat", "b": "ginger", "c": "rye"}}),
		jmaptest.Do("Jar/set", args{"create": args{"j": args{"name": "tin", "cookieIds": []string{"#c", "#a"}}}}),
		jmaptest.Do("Jar/get", args{"ids": []string{"#j"}, "properties": []string{"cookieIds"}}),
	)
	imported := r.Sentence(0).AssertNamed("Cookie/import").AsSet()
	checkEqual(t, "states of the import", []string{imported.OldState(), imported.NewState()}, []string{"1", "3"})
	checkJSON(t, "the item that failed, alone", imported.NotCreated("b")["type"], `"alreadyExists"`)
	checkJSON(t, "who made the rest", imported.Created("a")["bakedBy"], `"alice"`)
	r.Sentence(1).AsSet().AssertNoErrors()
	checkJSON(t, "the imported cookies, named by their creation ids in a later call", r.Sentence(2).Args["list"],
		`[{"id": "`+r.Sentence(1).AsSet().CreatedID("j")+`", "cookieIds": ["`+imported.CreatedID("c")+`", "`+imported.CreatedID("a")+`"]}]`)
	checkJSON(t, "the import is announced", stream.NextState(), `{"kitchen": {"Cookie": "3"}}`)
	checkGet(t, k, "what there is now", args{}, "ginger oat rye")
}
