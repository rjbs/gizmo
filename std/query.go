package std

import (
	"context"
	"encoding/json"

	"github.com/rjbs/gizmo/jmap"
)

// QueryRequest is the standard arguments of a /query.
type QueryRequest struct {
	AccountID string `json:"accountId"`

	// Filter is the filter as the client sent it, for the type to make
	// what it will of.  It is nil when there is none.
	Filter json.RawMessage `json:"filter"`

	// Sort is the comparators as the client sent them.  They are left
	// undecoded because a type may give its comparators properties of
	// their own, as RFC 8620 §5.5 allows.
	Sort []json.RawMessage `json:"sort"`

	Position     int     `json:"position"`
	Anchor       *string `json:"anchor"`
	AnchorOffset int     `json:"anchorOffset"`

	// Limit is the most ids the client wants, or nil for no limit.
	Limit          *int `json:"limit"`
	CalculateTotal bool `json:"calculateTotal"`
}

// ParseQuery decodes the arguments of a /query, in the way of ParseGet.
func ParseQuery(raw json.RawMessage, extra any) (*QueryRequest, error) {
	var req QueryRequest
	if err := decode(raw, &req, extra); err != nil {
		return nil, err
	}
	if string(req.Filter) == "null" {
		req.Filter = nil
	}
	if req.Limit != nil && *req.Limit < 0 {
		return nil, jmap.ArgError("limit must not be negative")
	}
	return &req, nil
}

// Comparator is a comparator with only the standard properties (RFC 8620
// §5.5), for a type that needs no more to decode QueryRequest.Sort into.
type Comparator struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending"`
	Collation   string `json:"collation"`
}

// Ascending reports the comparator's direction, which is ascending unless
// it says otherwise.
func (c Comparator) Ascending() bool { return c.IsAscending == nil || *c.IsAscending }

// Comparators decodes QueryRequest.Sort as standard comparators.  One with a
// property of its own is invalidArguments.
func (r *QueryRequest) Comparators() ([]Comparator, error) {
	out := make([]Comparator, len(r.Sort))
	for i, raw := range r.Sort {
		if err := jmap.DecodeArgs(raw, &out[i]); err != nil {
			return nil, err
		}
		if out[i].Property == "" {
			return nil, jmap.ArgError("a comparator needs a property")
		}
	}
	return out, nil
}

// Querier is what a type supplies for its /query.
type Querier[T Tx] struct {
	// Type is the type's name.
	Type string

	// All returns the id of every object the query matches, in order.
	// A filter or sort the type can't do is for it to refuse, with
	// unsupportedFilter or unsupportedSort.
	All func(ctx context.Context, tx T, req *QueryRequest) ([]string, error)

	// Page, if set, may answer for one page without finding every match:
	// the ids from position, at most limit of them, and the total.  It is
	// asked only when the page is wanted by position from the start.  It
	// may decline, by returning false, and All is asked instead.
	Page func(ctx context.Context, tx T, req *QueryRequest, position, limit int) (ids []string, total int, ok bool, err error)
}

// Query answers a /query.  It does not calculate query changes:
// canCalculateChanges is always false.
//
// An anchor given as a creation reference stands for the object created
// under that id earlier in the request.
func Query[T Tx](c *jmap.Call, begin Begin[T], limits Limits, q Querier[T], req *QueryRequest) (any, error) {
	ctx := c.Context()
	limits = limits.WithDefaults()

	limit, limited := limits.MaxQueryLimit, true
	if req.Limit != nil && *req.Limit <= limits.MaxQueryLimit {
		limit, limited = *req.Limit, false
	}

	tx, err := begin(ctx, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	state, err := tx.State(ctx, q.Type)
	if err != nil {
		return nil, err
	}

	var ids []string
	var total, position int
	paged := false
	if q.Page != nil && req.Anchor == nil && req.Position >= 0 {
		ids, total, paged, err = q.Page(ctx, tx, req, req.Position, limit)
		if err != nil {
			return nil, err
		}
		position = req.Position
	}
	if !paged {
		all, err := q.All(ctx, tx, req)
		if err != nil {
			return nil, err
		}
		total = len(all)
		position = req.Position
		if req.Anchor != nil {
			anchor, _ := c.ResolveID(*req.Anchor)
			idx := -1
			for i, id := range all {
				if id == anchor {
					idx = i
					break
				}
			}
			if idx < 0 {
				return nil, jmap.Errorf(jmap.ErrAnchorNotFound, "%q is not in the results", *req.Anchor)
			}
			position = max(idx+req.AnchorOffset, 0)
		} else if position < 0 {
			position = max(total+position, 0)
		}
		ids = []string{}
		if position < total {
			ids = all[position:min(position+limit, total)]
		}
	}
	if ids == nil {
		ids = []string{}
	}

	resp := map[string]any{
		"accountId":           req.AccountID,
		"queryState":          state,
		"canCalculateChanges": false,
		"position":            min(position, total),
		"ids":                 ids,
	}
	if req.CalculateTotal {
		resp["total"] = total
	}
	if limited && total-position > limit {
		resp["limit"] = limit
	}
	return resp, nil
}
