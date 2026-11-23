package std

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/rjbs/gizmo/jmap"
)

// SetRequest is the standard arguments of a /set.
type SetRequest struct {
	AccountID string  `json:"accountId"`
	IfInState *string `json:"ifInState"`

	// Create maps creation id to the object to create, and Update maps id
	// to the patch to apply, each as the client sent it.
	Create map[string]json.RawMessage `json:"create"`
	Update map[string]json.RawMessage `json:"update"`

	Destroy []string `json:"destroy"`
}

// ParseSet decodes the arguments of a /set, in the way of ParseGet.
func ParseSet(raw json.RawMessage, extra any) (*SetRequest, error) {
	var req SetRequest
	if err := decode(raw, &req, extra); err != nil {
		return nil, err
	}
	return &req, nil
}

// Setter is what a type supplies for its /set.  Each function does its work
// on one object, in a savepoint of the method's transaction.  When it returns
// a *jmap.SetError, whatever it wrote is undone, including anything it did to
// other objects, and the error is reported against that object alone.  Any
// other error fails the method, and nothing the method did is kept.
//
// A function that is nil refuses every object with a "forbidden" SetError.
type Setter[T Tx] struct {
	// Type is the type's name.
	Type string

	// Create makes an object from the properties the client gave.  It
	// returns the new object's id, and the properties the client should
	// be told of: those it didn't give, which the server set or
	// defaulted.  The id need not be among them.
	Create func(ctx context.Context, tx T, props json.RawMessage) (id string, created map[string]any, err error)

	// Update applies a patch to the object with the given id.  It returns
	// any properties that changed in a way the client can't know from its
	// own patch, or nil if there are none.  An id that names nothing is a
	// notFound SetError.
	Update func(ctx context.Context, tx T, id string, patch json.RawMessage) (updated map[string]any, err error)

	// Destroy removes the object with the given id.  An id that names
	// nothing is a notFound SetError.
	Destroy func(ctx context.Context, tx T, id string) error

	// Finish, if set, runs once every object has been dealt with, still
	// inside the transaction.  It may change the result, which is what
	// will be reported: to add what a further step changed, or to take
	// back a creation or update and say why.
	Finish func(ctx context.Context, tx T, r *SetResult) error
}

// SetResult is what a /set did, as it will be reported.
type SetResult struct {
	OldState, NewState string

	// Created maps creation id to what the client is told of the object,
	// which always includes its "id".
	Created map[string]map[string]any

	// Updated maps id to the properties to tell the client of, which is
	// nil when there are none.
	Updated map[string]map[string]any

	Destroyed []string

	// NotCreated is by creation id, and the other two by id as the client
	// gave it.
	NotCreated, NotUpdated, NotDestroyed map[string]*jmap.SetError
}

// Set answers a /set.
//
// Objects are destroyed, then created, then updated.  Destroying first
// frees whatever the destroyed objects held, such as a name that must be
// unique, for the objects made or changed in the same call.  Creating comes
// before updating because RFC 8620 §5.3 requires a creation to happen before
// anything in the call that refers to it, and an update may.
//
// An update or destroy may name an object by creation reference, "#" and
// the creation id of an object made earlier in the request or in this call.
// A destroy of an object made in this call waits until it has been made.
func Set[T Tx](c *jmap.Call, begin Begin[T], limits Limits, s Setter[T], req *SetRequest) (any, error) {
	limits = limits.WithDefaults()
	destroy := dedupe(req.Destroy)
	if len(req.Create)+len(req.Update)+len(destroy) > limits.MaxObjectsInSet {
		return nil, jmap.Errorf(jmap.ErrRequestTooLarge, "at most %d objects per /set", limits.MaxObjectsInSet)
	}

	return Transact(c, begin, func(w *Work[T]) (any, error) {
		ctx, tx := w.Context(), w.Tx
		r := &SetResult{
			Created:      map[string]map[string]any{},
			Updated:      map[string]map[string]any{},
			Destroyed:    []string{},
			NotCreated:   map[string]*jmap.SetError{},
			NotUpdated:   map[string]*jmap.SetError{},
			NotDestroyed: map[string]*jmap.SetError{},
		}
		var err error
		if r.OldState, err = tx.State(ctx, s.Type); err != nil {
			return nil, err
		}
		if req.IfInState != nil && *req.IfInState != r.OldState {
			return nil, jmap.Errorf(jmap.ErrStateMismatch, "state is %s", r.OldState)
		}

		forbidden := &jmap.SetError{Type: jmap.SetErrForbidden}

		destroyOne := func(key string) error {
			id, ok := c.ResolveID(key)
			if !ok {
				r.NotDestroyed[key] = &jmap.SetError{Type: jmap.SetErrNotFound, Description: "no object was created as " + key}
				return nil
			}
			if s.Destroy == nil {
				r.NotDestroyed[key] = forbidden
				return nil
			}
			serr, err := w.Do(func() error { return s.Destroy(ctx, tx, id) })
			if err != nil {
				return err
			}
			if serr != nil {
				r.NotDestroyed[key] = serr
				return nil
			}
			r.Destroyed = append(r.Destroyed, id)
			return nil
		}

		var later []string
		for _, key := range destroy {
			if _, known := c.ResolveID(key); !known && strings.HasPrefix(key, "#") {
				later = append(later, key)
				continue
			}
			if err := destroyOne(key); err != nil {
				return nil, err
			}
		}

		var made []string
		for _, cid := range sortedKeys(req.Create) {
			props := req.Create[cid]
			switch {
			case !isObject(props):
				r.NotCreated[cid] = jmap.InvalidProperties("the value to create must be an object")
				continue
			case s.Create == nil:
				r.NotCreated[cid] = forbidden
				continue
			}
			var id string
			var created map[string]any
			serr, err := w.Do(func() (err error) {
				id, created, err = s.Create(ctx, tx, props)
				return err
			})
			if err != nil {
				return nil, err
			}
			if serr != nil {
				r.NotCreated[cid] = serr
				continue
			}
			if created == nil {
				created = map[string]any{}
			}
			created["id"] = id
			r.Created[cid] = created
			c.SetCreatedID(cid, id)
			made = append(made, cid)
		}

		for _, key := range sortedKeys(req.Update) {
			patch := req.Update[key]
			id, ok := c.ResolveID(key)
			switch {
			case !ok:
				r.NotUpdated[key] = &jmap.SetError{Type: jmap.SetErrNotFound, Description: "no object was created as " + key}
				continue
			case !isObject(patch):
				r.NotUpdated[key] = jmap.InvalidProperties("the patch must be an object")
				continue
			case s.Update == nil:
				r.NotUpdated[key] = forbidden
				continue
			}
			var updated map[string]any
			serr, err := w.Do(func() (err error) {
				updated, err = s.Update(ctx, tx, id, patch)
				return err
			})
			if err != nil {
				return nil, err
			}
			if serr != nil {
				r.NotUpdated[key] = serr
				continue
			}
			if len(updated) == 0 {
				updated = nil
			}
			r.Updated[id] = updated
		}

		for _, key := range later {
			if err := destroyOne(key); err != nil {
				return nil, err
			}
		}

		if s.Finish != nil {
			if err := s.Finish(ctx, tx, r); err != nil {
				return nil, err
			}
			// A creation that Finish took back was never made, as far
			// as the rest of the request is concerned.
			for _, cid := range made {
				if _, kept := r.Created[cid]; !kept {
					c.ForgetCreatedID(cid)
				}
			}
		}

		if r.NewState, err = tx.State(ctx, s.Type); err != nil {
			return nil, err
		}
		return r.response(req.AccountID), nil
	})
}

// response gives the result the shape of a /set response, in which whatever
// is empty is null.
func (r *SetResult) response(accountID string) map[string]any {
	resp := map[string]any{
		"accountId":    accountID,
		"oldState":     r.OldState,
		"newState":     r.NewState,
		"created":      nil,
		"updated":      nil,
		"destroyed":    nil,
		"notCreated":   nil,
		"notUpdated":   nil,
		"notDestroyed": nil,
	}
	if len(r.Created) > 0 {
		resp["created"] = r.Created
	}
	if len(r.Updated) > 0 {
		resp["updated"] = r.Updated
	}
	if len(r.Destroyed) > 0 {
		resp["destroyed"] = r.Destroyed
	}
	if len(r.NotCreated) > 0 {
		resp["notCreated"] = r.NotCreated
	}
	if len(r.NotUpdated) > 0 {
		resp["notUpdated"] = r.NotUpdated
	}
	if len(r.NotDestroyed) > 0 {
		resp["notDestroyed"] = r.NotDestroyed
	}
	return resp
}
