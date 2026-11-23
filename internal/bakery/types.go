package bakery

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/rjbs/gizmo/auth"
	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/std"
)

// A Cookie has:
//
//   - id
//   - flavor: a string with something in it, which no other cookie in the
//     account may have
//   - notes: an object of whatever the client likes, {} by default
//   - bakedBy: the user who made it; server-set
//   - batch: the id of the API request that made it; server-set
//
// A Jar has:
//
//   - id
//   - name: a string with something in it
//   - cookieIds: the ids of the cookies in it, in order, [] by default
//
// Destroying a cookie takes it out of every jar it is in.

var cookieProperties = []string{"id", "flavor", "notes", "bakedBy", "batch"}
var jarProperties = []string{"id", "name", "cookieIds"}

func known(props []string) func(context.Context, string) bool {
	return func(_ context.Context, name string) bool { return slices.Contains(props, name) }
}

func (c *cookie) toJMAP(id string) map[string]any {
	notes := c.notes
	if notes == nil {
		notes = map[string]any{}
	}
	return map[string]any{"id": id, "flavor": c.flavor, "notes": notes, "bakedBy": c.bakedBy, "batch": c.batch}
}

func (j *jar) toJMAP(id string) map[string]any {
	ids := j.cookieIDs
	if ids == nil {
		ids = []string{}
	}
	return map[string]any{"id": id, "name": j.name, "cookieIds": ids}
}

// load returns the objects with the given ids, or all of them, from a map of
// objects that each know how to show themselves.
func load[O interface{ toJMAP(string) map[string]any }](all map[string]O, ids []string) (map[string]map[string]any, []string, error) {
	if ids == nil {
		for id := range all {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	out, found := map[string]map[string]any{}, []string{}
	for _, id := range ids {
		if o, ok := all[id]; ok {
			out[id] = o.toJMAP(id)
			found = append(found, id)
		}
	}
	return out, found, nil
}

// ---- Cookie ---------------------------------------------------------------

func (a *Account) cookieGet(c *jmap.Call, raw json.RawMessage) (any, error) {
	// shout is an argument of the bakery's own: it has flavors given in
	// capitals.
	var extra struct {
		Shout bool `json:"shout"`
	}
	req, err := std.ParseGet(raw, &extra)
	if err != nil {
		return nil, err
	}
	return std.Get(c, a.begin, a.limits, std.Getter[*tx]{
		Type:          "Cookie",
		KnownProperty: known(cookieProperties),
		Count:         func(_ context.Context, t *tx) (int, error) { return len(t.d.cookies), nil },
		Load: func(_ context.Context, t *tx, ids, _ []string) (map[string]map[string]any, []string, error) {
			objects, found, err := load(t.d.cookies, ids)
			if extra.Shout {
				for _, o := range objects {
					o["flavor"] = strings.ToUpper(o["flavor"].(string))
				}
			}
			return objects, found, err
		},
	}, req)
}

// checkCookie checks a cookie's properties, given as a whole object, against
// the cookie it would replace, if any, and returns the cookie they describe.
func checkCookie(d *data, id string, old *cookie, props map[string]any) (*cookie, error) {
	var bad []string
	for name := range props {
		if !slices.Contains(cookieProperties, name) {
			bad = append(bad, name)
		}
	}
	out := &cookie{}
	if old != nil {
		*out = *old
	}
	// What the server sets may be given only as it already is.
	for name, have := range map[string]string{"id": id, "bakedBy": out.bakedBy, "batch": out.batch} {
		if given, ok := props[name]; ok && (old == nil || given != have) {
			bad = append(bad, name)
		}
	}
	flavor, ok := props["flavor"].(string)
	if !ok || strings.TrimSpace(flavor) == "" {
		bad = append(bad, "flavor")
	}
	out.flavor = flavor
	switch notes := props["notes"].(type) {
	case nil:
		out.notes = map[string]any{}
	case map[string]any:
		out.notes = notes
	default:
		bad = append(bad, "notes")
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.InvalidProperties("these properties can't be set, or not to that", bad...)
	}
	for other, c := range d.cookies {
		if other != id && c.flavor == flavor {
			return nil, jmap.AlreadyExists("there is already a "+flavor+" cookie", other)
		}
	}
	return out, nil
}

// createCookie makes a cookie, as Cookie/set and Cookie/import both do.
func createCookie(ctx context.Context, t *tx, props map[string]any) (string, map[string]any, error) {
	ck, err := checkCookie(t.d, "", nil, props)
	if err != nil {
		return "", nil, err
	}
	if user, ok := auth.UserFrom(ctx); ok {
		ck.bakedBy = user.ID
	}
	ck.batch = jmap.RequestID(ctx)
	id := t.d.newID()
	ck.created = t.d.bump("Cookie")
	ck.modified = ck.created
	t.d.cookies[id] = ck

	told := map[string]any{"bakedBy": ck.bakedBy, "batch": ck.batch}
	if _, given := props["notes"]; !given {
		told["notes"] = map[string]any{}
	}
	return id, told, nil
}

func (a *Account) cookieSet(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseSet(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Set(c, a.begin, a.limits, std.Setter[*tx]{
		Type: "Cookie",
		Create: func(ctx context.Context, t *tx, raw json.RawMessage) (string, map[string]any, error) {
			props, err := jmap.DecodeObject(raw)
			if err != nil {
				return "", nil, err
			}
			return createCookie(ctx, t, props)
		},
		Update: func(_ context.Context, t *tx, id string, raw json.RawMessage) (map[string]any, error) {
			old, ok := t.d.cookies[id]
			if !ok {
				return nil, jmap.NotFound()
			}
			patch, err := jmap.DecodeObject(raw)
			if err != nil {
				return nil, err
			}
			props := old.toJMAP(id)
			if serr := jmap.ApplyPatch(props, patch); serr != nil {
				return nil, serr
			}
			ck, err := checkCookie(t.d, id, old, props)
			if err != nil {
				return nil, err
			}
			ck.modified = t.d.bump("Cookie")
			t.d.cookies[id] = ck
			return nil, nil
		},
		Destroy: func(_ context.Context, t *tx, id string) error {
			old, ok := t.d.cookies[id]
			if !ok {
				return jmap.NotFound()
			}
			// A cookie that says it is cursed takes itself out of
			// its jars and then can't be destroyed after all, to
			// show that the jars are put back as they were.
			cursed := old.notes["cursed"] == true
			for _, j := range t.d.jars {
				if i := slices.Index(j.cookieIDs, id); i >= 0 {
					j.cookieIDs = slices.DeleteFunc(j.cookieIDs, func(c string) bool { return c == id })
					j.modified = t.d.bump("Jar")
				}
			}
			if cursed {
				return &jmap.SetError{Type: "cursed", Description: "this cookie can't be destroyed"}
			}
			delete(t.d.cookies, id)
			t.d.graves = append(t.d.graves, grave{typ: "Cookie", id: id, created: old.created, modified: t.d.bump("Cookie")})
			return nil
		},
	}, req)
}

func (a *Account) cookieChanges(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseChanges(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Changes(c, a.begin, req, func(_ context.Context, t *tx, since string, max int) (*std.Changed, error) {
		from, err := strconv.Atoi(since)
		current := t.d.modseq["Cookie"]
		if err != nil || from < 0 || from > current {
			return nil, jmap.Errorf(jmap.ErrCannotCalculateChanges, "state %q is not one of this account's", since)
		}
		type change struct {
			id                string
			created, modified int
			gone              bool
		}
		var all []change
		for id, ck := range t.d.cookies {
			if ck.modified > from {
				all = append(all, change{id, ck.created, ck.modified, false})
			}
		}
		for _, g := range t.d.graves {
			if g.typ == "Cookie" && g.modified > from {
				all = append(all, change{g.id, g.created, g.modified, true})
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].modified < all[j].modified })

		ch := &std.Changed{NewState: strconv.Itoa(current)}
		for i, c := range all {
			if max > 0 && i == max {
				ch.HasMore = true
				ch.NewState = strconv.Itoa(all[i-1].modified)
				break
			}
			switch {
			case c.gone && c.created > from:
			case c.gone:
				ch.Destroyed = append(ch.Destroyed, c.id)
			case c.created > from:
				ch.Created = append(ch.Created, c.id)
			default:
				ch.Updated = append(ch.Updated, c.id)
			}
		}
		return ch, nil
	})
}

// Cookie/query filters on {"flavorContains": string} and sorts on "flavor"
// or, by default, "id".
func (a *Account) cookieQuery(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseQuery(raw, nil)
	if err != nil {
		return nil, err
	}
	sorted := func(t *tx, req *std.QueryRequest, contains string) ([]string, error) {
		comparators, err := req.Comparators()
		if err != nil {
			return nil, err
		}
		var ids []string
		for id, ck := range t.d.cookies {
			if strings.Contains(ck.flavor, contains) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for i := len(comparators) - 1; i >= 0; i-- {
			cmp := comparators[i]
			if cmp.Property != "flavor" && cmp.Property != "id" {
				return nil, jmap.Errorf(jmap.ErrUnsupportedSort, "cookies can't be sorted by %s", cmp.Property)
			}
			key := func(id string) string {
				if cmp.Property == "flavor" {
					return t.d.cookies[id].flavor
				}
				return id
			}
			sort.SliceStable(ids, func(i, j int) bool {
				if cmp.Ascending() {
					return key(ids[i]) < key(ids[j])
				}
				return key(ids[i]) > key(ids[j])
			})
		}
		return ids, nil
	}
	return std.Query(c, a.begin, a.limits, std.Querier[*tx]{
		Type: "Cookie",
		All: func(_ context.Context, t *tx, req *std.QueryRequest) ([]string, error) {
			var filter struct {
				FlavorContains string `json:"flavorContains"`
			}
			if req.Filter != nil {
				if err := jmap.DecodeArgs(req.Filter, &filter); err != nil {
					return nil, jmap.Errorf(jmap.ErrUnsupportedFilter, "%v", err)
				}
			}
			return sorted(t, req, filter.FlavorContains)
		},
		// With no filter, a page can be had without the name of every
		// match being handed back, or so the bakery pretends, to
		// exercise the way through Page.
		Page: func(_ context.Context, t *tx, req *std.QueryRequest, position, limit int) ([]string, int, bool, error) {
			if req.Filter != nil {
				return nil, 0, false, nil
			}
			ids, err := sorted(t, req, "")
			if err != nil {
				return nil, 0, false, err
			}
			total := len(ids)
			return ids[min(position, total):min(position+limit, total)], total, true, nil
		},
	}, req)
}

// cookieImport is Cookie/import, a method of the bakery's own, which makes
// cookies from a map of creation id to flavor.  It answers as a /set does,
// with created and notCreated, and is here to show that a method the
// application writes can do what /set does.
func (a *Account) cookieImport(c *jmap.Call, raw json.RawMessage) (any, error) {
	var args struct {
		AccountID string            `json:"accountId"`
		Flavors   map[string]string `json:"flavors"`
	}
	if err := jmap.DecodeArgs(raw, &args); err != nil {
		return nil, err
	}
	return std.Transact(c, a.begin, func(w *std.Work[*tx]) (any, error) {
		ctx := w.Context()
		oldState, _ := w.Tx.State(ctx, "Cookie")
		created, notCreated := map[string]any{}, map[string]any{}
		cids := make([]string, 0, len(args.Flavors))
		for cid := range args.Flavors {
			cids = append(cids, cid)
		}
		sort.Strings(cids)
		for _, cid := range cids {
			var id string
			var told map[string]any
			serr, err := w.Do(func() (err error) {
				id, told, err = createCookie(ctx, w.Tx, map[string]any{"flavor": args.Flavors[cid]})
				return err
			})
			if err != nil {
				return nil, err
			}
			if serr != nil {
				notCreated[cid] = serr
				continue
			}
			told["id"] = id
			created[cid] = told
			c.SetCreatedID(cid, id)
		}
		newState, _ := w.Tx.State(ctx, "Cookie")
		return map[string]any{
			"accountId": args.AccountID, "oldState": oldState, "newState": newState,
			"created": created, "notCreated": notCreated,
		}, nil
	})
}

// ---- Jar ------------------------------------------------------------------

func (a *Account) jarGet(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseGet(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Get(c, a.begin, a.limits, std.Getter[*tx]{
		Type:          "Jar",
		KnownProperty: known(jarProperties),
		Load: func(_ context.Context, t *tx, ids, _ []string) (map[string]map[string]any, []string, error) {
			return load(t.d.jars, ids)
		},
	}, req)
}

// checkJar checks a jar's properties, given as a whole object, and returns
// the jar they describe.  A cookie id may be a creation reference.
func checkJar(c *jmap.Call, d *data, props map[string]any) (*jar, error) {
	var bad []string
	for name := range props {
		if name != "id" && !slices.Contains(jarProperties, name) {
			bad = append(bad, name)
		}
	}
	out := &jar{}
	var ok bool
	if out.name, ok = props["name"].(string); !ok || strings.TrimSpace(out.name) == "" {
		bad = append(bad, "name")
	}
	switch ids := props["cookieIds"].(type) {
	case nil:
	case []any:
		for _, v := range ids {
			id, _ := v.(string)
			real, found := c.ResolveID(id)
			if _, exists := d.cookies[real]; !found || !exists {
				bad = append(bad, "cookieIds")
				break
			}
			out.cookieIDs = append(out.cookieIDs, real)
		}
	default:
		bad = append(bad, "cookieIds")
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.InvalidProperties("these properties can't be set, or not to that", bad...)
	}
	return out, nil
}

func (a *Account) jarSet(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseSet(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Set(c, a.begin, a.limits, std.Setter[*tx]{
		Type: "Jar",
		Create: func(_ context.Context, t *tx, raw json.RawMessage) (string, map[string]any, error) {
			props, err := jmap.DecodeObject(raw)
			if err != nil {
				return "", nil, err
			}
			if _, given := props["id"]; given {
				return "", nil, jmap.InvalidProperties("the server chooses the id", "id")
			}
			j, err := checkJar(c, t.d, props)
			if err != nil {
				return "", nil, err
			}
			id := t.d.newID()
			j.created = t.d.bump("Jar")
			j.modified = j.created
			t.d.jars[id] = j
			told := map[string]any{}
			if _, given := props["cookieIds"]; !given {
				told["cookieIds"] = []string{}
			}
			return id, told, nil
		},
		Update: func(_ context.Context, t *tx, id string, raw json.RawMessage) (map[string]any, error) {
			old, ok := t.d.jars[id]
			if !ok {
				return nil, jmap.NotFound()
			}
			patch, err := jmap.DecodeObject(raw)
			if err != nil {
				return nil, err
			}
			// The round trip through JSON gives the patch the same
			// shapes to work on as it has itself.
			var props map[string]any
			b, _ := json.Marshal(old.toJMAP(id))
			json.Unmarshal(b, &props)
			if serr := jmap.ApplyPatch(props, patch); serr != nil {
				return nil, serr
			}
			if props["id"] != id {
				return nil, jmap.InvalidProperties("the id can't be changed", "id")
			}
			j, err := checkJar(c, t.d, props)
			if err != nil {
				return nil, err
			}
			j.created, j.modified = old.created, t.d.bump("Jar")
			t.d.jars[id] = j
			return nil, nil
		},
		// A jar can't be destroyed: there is no Destroy.
	}, req)
}
