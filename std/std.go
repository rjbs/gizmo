// Package std implements JMAP's standard methods (RFC 8620 §5) over a data
// type that the application describes with a few functions: /get, /changes,
// /set and /query.
//
// The package owns what is the same for every type: the arguments, the
// limits, paging, ifInState, creation ids, and the shape of the response.
// The type owns its data: loading objects, checking and writing them, and
// turning a filter and sort into results.  The package never sees a
// database.  It asks the application to begin a transaction, of the
// application's own kind, and hands that to the type's functions.
//
// The same parts are there for an application's own methods.  One that
// writes, like an /import, can use Transact and Work to get what /set has: a
// transaction for the call, and a savepoint for each item in it.
package std

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/rjbs/gizmo/jmap"
)

// Tx is a transaction on one account's data, as much of one as this package
// needs to know about.  The application's transaction type will have a good
// deal more to it, for the type's own functions to use.
type Tx interface {
	// State returns the current state string of a type, as the
	// transaction sees it.
	State(ctx context.Context, typ string) (string, error)

	// Savepoint runs fn so that whatever it wrote is undone if it returns
	// an error, and returns fn's error.
	Savepoint(ctx context.Context, fn func() error) error

	// Commit makes the transaction's writes permanent.  It is also the
	// time to tell the world of them, as by publishing new states.
	Commit() error

	// Rollback abandons the transaction.  It must be safe to call after
	// Commit, when it does nothing.
	Rollback() error
}

// Begin begins a transaction on the account a call is for.  A method that
// only reads asks for one that need not be able to write.
type Begin[T Tx] func(ctx context.Context, write bool) (T, error)

// Limits are the limits the standard methods enforce: the most objects in a
// /get and in a /set, and the most ids from a /query.  They are the same
// value that the HTTP server is given to advertise, so that what is
// advertised is what is kept.
type Limits = jmap.Limits

// Work is a write transaction in progress for one method call.
type Work[T Tx] struct {
	// Tx is the application's transaction.
	Tx T

	ctx context.Context
}

// Context returns the context of the call the work is for.
func (w *Work[T]) Context() context.Context { return w.ctx }

// Do runs one item of the work, such as the creation of one object, in a
// savepoint.  If fn returns a *jmap.SetError, what it wrote is undone and
// the SetError is returned for the method to report against that item; the
// rest of the work stands.  Any other error is returned as the second value,
// and should end the method.
func (w *Work[T]) Do(fn func() error) (*jmap.SetError, error) {
	err := w.Tx.Savepoint(w.ctx, fn)
	var serr *jmap.SetError
	if errors.As(err, &serr) {
		return serr, nil
	}
	return nil, err
}

// Transact runs fn in a write transaction for the call, which is committed
// if fn returns no error and abandoned if it does.  What fn returns is the
// method's response.
func Transact[T Tx](c *jmap.Call, begin Begin[T], fn func(w *Work[T]) (any, error)) (any, error) {
	ctx := c.Context()
	tx, err := begin(ctx, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := fn(&Work[T]{Tx: tx, ctx: ctx})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// decode splits a method's arguments between the standard ones, which go
// into known, and any others, which go into extra.  Each is a pointer to a
// struct.  With no extra, or for an argument that neither has a field for,
// the call is invalidArguments.
func decode(raw json.RawMessage, known, extra any) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return jmap.ArgError("arguments must be an object")
	}
	mine, theirs := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	names := fieldNames(known)
	for name, value := range all {
		if names[name] {
			mine[name] = value
		} else {
			theirs[name] = value
		}
	}
	if err := strict(mine, known); err != nil {
		return err
	}
	if extra == nil {
		if len(theirs) > 0 {
			return jmap.ArgError("unknown argument %q", sortedKeys(theirs)[0])
		}
		return nil
	}
	return strict(theirs, extra)
}

func strict(args map[string]json.RawMessage, into any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return jmap.ArgError("%v", err)
	}
	return nil
}

// fieldNames gives the JSON names of a struct's fields, by encoding its zero
// value: none of the argument structs here omits a field.
func fieldNames(v any) map[string]bool {
	raw, _ := json.Marshal(v)
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	names := map[string]bool{}
	for name := range fields {
		names[name] = true
	}
	return names
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// isObject reports whether raw is a JSON object.
func isObject(raw json.RawMessage) bool {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	return len(raw) > 0 && raw[0] == '{'
}
