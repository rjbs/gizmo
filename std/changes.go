package std

import (
	"context"
	"encoding/json"

	"github.com/rjbs/gizmo/jmap"
)

// ChangesRequest is the standard arguments of a /changes.
type ChangesRequest struct {
	AccountID  string `json:"accountId"`
	SinceState string `json:"sinceState"`

	// MaxChanges is the most ids the client wants, or nil for no limit.
	MaxChanges *int `json:"maxChanges"`
}

// ParseChanges decodes the arguments of a /changes, in the way of [ParseGet].
func ParseChanges(raw json.RawMessage, extra any) (*ChangesRequest, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err == nil && all["sinceState"] == nil {
		return nil, jmap.ArgError("sinceState is required")
	}
	var req ChangesRequest
	if err := decode(raw, &req, extra); err != nil {
		return nil, err
	}
	if req.MaxChanges != nil && *req.MaxChanges <= 0 {
		return nil, jmap.ArgError("maxChanges must be positive")
	}
	return &req, nil
}

// Changed is what has changed in a type since some state.
type Changed struct {
	// NewState is the state the changes bring the client to.  When there
	// are more changes than were asked for, this is a state part of the
	// way there, and HasMore is true.
	NewState string
	HasMore  bool

	Created, Updated, Destroyed []string
}

// Changes answers a /changes.  The finding of the changes is the type's own:
// find is given the state the client has and the most changes it wants, zero
// meaning any number.  When the changes since that state can't be worked out,
// find returns a cannotCalculateChanges method error.
func Changes[T Tx](
	c *jmap.Call, begin Begin[T], req *ChangesRequest,
	find func(ctx context.Context, tx T, since string, max int) (*Changed, error),
) (any, error) {
	ctx := c.Context()
	tx, err := begin(ctx, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	max := 0
	if req.MaxChanges != nil {
		max = *req.MaxChanges
	}
	ch, err := find(ctx, tx, req.SinceState, max)
	if err != nil {
		return nil, err
	}
	list := func(ids []string) []string {
		if ids == nil {
			return []string{}
		}
		return ids
	}
	return map[string]any{
		"accountId":      req.AccountID,
		"oldState":       req.SinceState,
		"newState":       ch.NewState,
		"hasMoreChanges": ch.HasMore,
		"created":        list(ch.Created),
		"updated":        list(ch.Updated),
		"destroyed":      list(ch.Destroyed),
	}, nil
}
