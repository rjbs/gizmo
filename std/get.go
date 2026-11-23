package std

import (
	"context"
	"encoding/json"

	"github.com/rjbs/gizmo/jmap"
)

// GetRequest is the standard arguments of a /get.
type GetRequest struct {
	AccountID string `json:"accountId"`

	// IDs is the ids asked for, or nil for every object.
	IDs *[]string `json:"ids"`

	// Properties is the properties asked for, or nil for the default.
	Properties *[]string `json:"properties"`
}

// ParseGet decodes the arguments of a /get.  Arguments that aren't standard
// are decoded into extra, a pointer to a struct, if it is given; otherwise
// they are refused.
func ParseGet(raw json.RawMessage, extra any) (*GetRequest, error) {
	var req GetRequest
	if err := decode(raw, &req, extra); err != nil {
		return nil, err
	}
	return &req, nil
}

// Getter is what a type supplies for its /get.
type Getter[T Tx] struct {
	// Type is the type's name, like "Email".
	Type string

	// KnownProperty reports whether the type has a property of the given
	// name, as far as this request may see.  Asking for one it doesn't
	// have is invalidArguments.  If nil, any name may be asked for.
	KnownProperty func(ctx context.Context, name string) bool

	// DefaultProperties lists the properties returned when the request
	// names none.  If nil, an object is returned as Load gave it.
	DefaultProperties func(ctx context.Context) []string

	// Count returns how many objects there are, so that a request for
	// all of them can be refused, when there are too many, before any is
	// loaded.  If nil, they are loaded and then counted.
	Count func(ctx context.Context, tx T) (int, error)

	// Load returns the objects with the given ids, by id, or every object
	// when ids is nil, along with the ids found in the order they should
	// be listed.  Each object must have its "id".  props is the properties
	// wanted, or nil for all; Load may return more than was wanted, and
	// they will be left out of the response.
	Load func(ctx context.Context, tx T, ids, props []string) (objects map[string]map[string]any, found []string, err error)
}

// Get answers a /get.
//
// An id given as a creation reference, "#" and a creation id, stands for
// the object created under that id earlier in the request.
func Get[T Tx](c *jmap.Call, begin Begin[T], limits Limits, g Getter[T], req *GetRequest) (any, error) {
	ctx := c.Context()
	limits = limits.WithDefaults()

	var props []string
	if req.Properties != nil {
		props = []string{"id"}
		for _, p := range dedupe(*req.Properties) {
			if g.KnownProperty != nil && !g.KnownProperty(ctx, p) {
				return nil, jmap.ArgError("%s has no property %q", g.Type, p)
			}
			if p != "id" {
				props = append(props, p)
			}
		}
	} else if g.DefaultProperties != nil {
		props = g.DefaultProperties(ctx)
	}

	// asked pairs each id as the client gave it with the id it stands
	// for, which differs for a creation reference.
	type asked struct{ given, real string }
	var want []asked
	var ids []string
	notFound := []string{}
	if req.IDs != nil {
		given := dedupe(*req.IDs)
		if len(given) > limits.MaxObjectsInGet {
			return nil, jmap.Errorf(jmap.ErrRequestTooLarge, "at most %d ids per /get", limits.MaxObjectsInGet)
		}
		ids = []string{}
		for _, id := range given {
			real, ok := c.ResolveID(id)
			if !ok {
				notFound = append(notFound, id)
				continue
			}
			want = append(want, asked{id, real})
			ids = append(ids, real)
		}
	}

	tx, err := begin(ctx, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if req.IDs == nil && g.Count != nil {
		n, err := g.Count(ctx, tx)
		if err != nil {
			return nil, err
		}
		if n > limits.MaxObjectsInGet {
			return nil, jmap.Errorf(jmap.ErrRequestTooLarge, "%d objects; at most %d per /get", n, limits.MaxObjectsInGet)
		}
	}

	// State is read before the objects.  If the transaction is not a
	// snapshot and a write lands between the two, the client gets newer
	// objects with an older state, and its next /changes repeats a few
	// updates; the reverse would lose them.  -- claude, 2026-10-24
	state, err := tx.State(ctx, g.Type)
	if err != nil {
		return nil, err
	}
	objects, found, err := g.Load(ctx, tx, ids, props)
	if err != nil {
		return nil, err
	}
	if req.IDs == nil {
		if len(found) > limits.MaxObjectsInGet {
			return nil, jmap.Errorf(jmap.ErrRequestTooLarge, "%d objects; at most %d per /get", len(found), limits.MaxObjectsInGet)
		}
		for _, id := range found {
			want = append(want, asked{id, id})
		}
	}

	list := []map[string]any{}
	for _, w := range want {
		obj, ok := objects[w.real]
		if !ok {
			notFound = append(notFound, w.given)
			continue
		}
		if props != nil {
			only := make(map[string]any, len(props))
			for _, p := range props {
				if v, has := obj[p]; has {
					only[p] = v
				}
			}
			obj = only
		}
		list = append(list, obj)
	}
	return map[string]any{
		"accountId": req.AccountID,
		"state":     state,
		"list":      list,
		"notFound":  notFound,
	}, nil
}
