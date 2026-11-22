package auth

import (
	"encoding/json"

	"github.com/rjbs/gizmo/jmap"
)

// ForAccount makes a method of one that needs an account's backend, whatever
// the application takes that to be: its database, or a service over it.  The
// method made reads the accountId from its arguments, finds that account's
// backend, and hands the call on.
//
// An account the caller doesn't own gets the same accountNotFound as one that
// doesn't exist, so that a caller can't learn which accounts there are.  A
// call that names no account is invalidArguments.
func ForAccount[B any](
	find func(accountID string) (B, bool),
	handle func(backend B, c *jmap.Call, args json.RawMessage) (any, error),
) jmap.Handler {
	return func(c *jmap.Call, args json.RawMessage) (any, error) {
		id, err := jmap.AccountID(args)
		if err != nil {
			return nil, err
		}
		user, _ := UserFrom(c.Context())
		if user == nil || !user.HasAccount(id) {
			return nil, &jmap.MethodError{Type: jmap.ErrAccountNotFound}
		}
		backend, ok := find(id)
		if !ok {
			return nil, &jmap.MethodError{Type: jmap.ErrAccountNotFound}
		}
		return handle(backend, c, args)
	}
}
