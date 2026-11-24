package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// Limits are the limits a server gives in its core capability (RFC 8620
// §2).  A client keeps its requests within them: Batch does for the calls
// in a request, and slices.Chunk will split a list of ids for /get or /set.
type Limits struct {
	MaxSizeUpload         int64 `json:"maxSizeUpload"`
	MaxConcurrentUpload   int   `json:"maxConcurrentUpload"`
	MaxSizeRequest        int64 `json:"maxSizeRequest"`
	MaxConcurrentRequests int   `json:"maxConcurrentRequests"`
	MaxCallsInRequest     int   `json:"maxCallsInRequest"`
	MaxObjectsInGet       int   `json:"maxObjectsInGet"`
	MaxObjectsInSet       int   `json:"maxObjectsInSet"`
}

// Limits returns the limits the session gives.  Where the server doesn't
// say how many calls a request may have, or how many objects a /get or a
// /set, the answer is the least that the RFC suggests a server allow: 16
// calls and 500 objects.
func (s *Session) Limits() Limits {
	var l Limits
	json.Unmarshal(s.Capabilities["urn:ietf:params:jmap:core"], &l)
	if l.MaxCallsInRequest <= 0 {
		l.MaxCallsInRequest = 16
	}
	if l.MaxObjectsInGet <= 0 {
		l.MaxObjectsInGet = 500
	}
	if l.MaxObjectsInSet <= 0 {
		l.MaxObjectsInSet = 500
	}
	return l
}

// Batch makes the calls of every group, in order, in as few requests as
// the server's maxCallsInRequest allows.  A group is never split between
// requests, so its calls may refer to one another's results; calls of
// different groups may not, and nor may they refer to one another's
// creation ids.
//
// The answer is one Response, as if there had been one request: the results
// of all, in order, and the created ids of all.  A call given no id gets
// its place among all the calls of the batch, "0", "1", and so on.  If a
// request fails, the requests before it have still been made, and their
// results are lost; a caller that can't have that makes each Request
// itself.
//
// This is after the Batch of the JMAP client in Loose Threads.
// -- claude, 2026-11-23
func (c *Client) Batch(ctx context.Context, groups ...[]Call) (*Response, error) {
	s, err := c.Session(ctx)
	if err != nil {
		return nil, err
	}
	most := s.Limits().MaxCallsInRequest

	out := &Response{}
	var calls []Call
	flush := func() error {
		if len(calls) == 0 {
			return nil
		}
		resp, err := c.Request(ctx, calls...)
		if err != nil {
			return err
		}
		out.Results = append(out.Results, resp.Results...)
		out.SessionState = resp.SessionState
		for cid, id := range resp.CreatedIDs {
			if out.CreatedIDs == nil {
				out.CreatedIDs = map[string]string{}
			}
			out.CreatedIDs[cid] = id
		}
		calls = nil
		return nil
	}

	n := 0
	for _, group := range groups {
		if len(group) > most {
			return nil, fmt.Errorf("%d calls belong together, but the server allows only %d in a request", len(group), most)
		}
		if len(calls)+len(group) > most {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		for _, call := range group {
			if call.ID == "" {
				call.ID = strconv.Itoa(n)
			}
			n++
			calls = append(calls, call)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}
