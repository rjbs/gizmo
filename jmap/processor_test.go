package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
)

const testCapability = "https://gizmo.example/test"

// testProcessor has a few methods under testCapability:
//
//   - Test/make creates an "object" for each entry in its create argument,
//     recording creation ids, and answers like a /set would.  It thinks
//     better of any whose id is "regret".
//   - Test/ids answers with the ids it was given, creation references
//     resolved.
//   - Test/twice answers and then adds a second response, Test/again.
//   - Test/fail fails with a method error, and Test/break with a plain one.
//   - Test/whoami answers with the request's id and whether it uses "extra".
func testProcessor() *Processor {
	p := NewProcessor()
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.AddCapability("https://gizmo.example/extra")
	p.MaxCallsInRequest = 3

	p.Register("Test/make", testCapability, func(c *Call, args json.RawMessage) (any, error) {
		var a struct {
			Create map[string]struct {
				ID string `json:"id"`
			} `json:"create"`
		}
		if err := DecodeArgs(args, &a); err != nil {
			return nil, err
		}
		created := map[string]any{}
		for cid, o := range a.Create {
			c.SetCreatedID(cid, o.ID)
			if o.ID == "regret" {
				c.ForgetCreatedID(cid)
				continue
			}
			created[cid] = map[string]any{"id": o.ID}
		}
		return map[string]any{"created": created}, nil
	})
	p.Register("Test/ids", testCapability, func(c *Call, args json.RawMessage) (any, error) {
		var a struct {
			IDs []string `json:"ids"`
		}
		if err := DecodeArgs(args, &a); err != nil {
			return nil, err
		}
		out := []string{}
		for _, id := range a.IDs {
			real, ok := c.ResolveID(id)
			if !ok {
				return nil, ArgError("nothing was created as %s", id)
			}
			out = append(out, real)
		}
		return map[string]any{"ids": out}, nil
	})
	p.Register("Test/twice", testCapability, func(c *Call, _ json.RawMessage) (any, error) {
		c.Respond("Test/again", map[string]any{"n": 2})
		return map[string]any{"n": 1}, nil
	})
	p.Register("Test/fail", testCapability, func(*Call, json.RawMessage) (any, error) {
		return nil, Errorf(ErrForbidden, "no")
	})
	p.Register("Test/break", testCapability, func(*Call, json.RawMessage) (any, error) {
		return nil, errors.New("the password is swordfish")
	})
	p.Register("Test/whoami", testCapability, func(c *Call, _ json.RawMessage) (any, error) {
		return map[string]any{
			"request": RequestID(c.Context()),
			"extra":   Using(c.Context(), "https://gizmo.example/extra"),
		}, nil
	})
	return p
}

// process runs a request given as JSON and returns the response as JSON.
func process(t *testing.T, request string) string {
	t.Helper()
	req, rerr := ParseRequest([]byte(request))
	if rerr != nil {
		t.Fatalf("parsing %s: %v", request, rerr)
	}
	resp, rerr := testProcessor().Process(context.Background(), req)
	if rerr != nil {
		t.Fatalf("processing %s: %v", request, rerr)
	}
	out, err := json.Marshal(resp.MethodResponses)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// checkResponses runs the method calls, which use core and testCapability,
// and compares the method responses with want.
func checkResponses(t *testing.T, name, calls, want string) {
	t.Helper()
	got := process(t, `{"using":["`+CoreCapability+`","`+testCapability+`"],"methodCalls":`+calls+`}`)
	if !sameJSON(t, got, want) {
		t.Errorf("%s:\n got %s\nwant %s", name, got, want)
	}
}

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("bad JSON %s: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("bad JSON %s: %v", b, err)
	}
	p, _ := json.Marshal(x)
	q, _ := json.Marshal(y)
	return string(p) == string(q)
}

// checkRequestError checks that a request body is refused as a whole, with
// the given error type and, if the error is a limit, the limit's name.
func checkRequestError(t *testing.T, name, body, wantType, wantLimit string) {
	t.Helper()
	req, rerr := ParseRequest([]byte(body))
	if rerr == nil {
		_, rerr = testProcessor().Process(context.Background(), req)
	}
	switch {
	case rerr == nil:
		t.Errorf("%s: expected %s, but the request was accepted", name, wantType)
	case rerr.Type != wantType || rerr.Limit != wantLimit:
		t.Errorf("%s: got %s (limit %q), want %s (limit %q)", name, rerr.Type, rerr.Limit, wantType, wantLimit)
	}
}

func TestDispatch(t *testing.T) {
	checkResponses(t, "echo",
		`[["Core/echo", {"hello": true, "n": 1.50}, "a"]]`,
		`[["Core/echo", {"hello": true, "n": 1.50}, "a"]]`)
	checkResponses(t, "calls run in order, each with its own call id",
		`[["Core/echo", {"n": 1}, "a"], ["Core/echo", {"n": 2}, "b"]]`,
		`[["Core/echo", {"n": 1}, "a"], ["Core/echo", {"n": 2}, "b"]]`)
	checkResponses(t, "an unknown method",
		`[["Nope/get", {}, "a"]]`,
		`[["error", {"type": "unknownMethod", "description": "no method Nope/get with the capabilities in use"}, "a"]]`)
	checkResponses(t, "a method error",
		`[["Test/fail", {}, "a"]]`,
		`[["error", {"type": "forbidden", "description": "no"}, "a"]]`)
	checkResponses(t, "any other error is serverFail, and says nothing of itself",
		`[["Test/break", {}, "a"]]`,
		`[["error", {"type": "serverFail", "description": "internal error"}, "a"]]`)
	checkResponses(t, "an unknown argument",
		`[["Test/ids", {"ids": [], "bogus": 1}, "a"]]`,
		`[["error", {"type": "invalidArguments", "description": "json: unknown field \"bogus\""}, "a"]]`)
	checkResponses(t, "a failed call doesn't stop the ones after it",
		`[["Test/fail", {}, "a"], ["Core/echo", {}, "b"]]`,
		`[["error", {"type": "forbidden", "description": "no"}, "a"], ["Core/echo", {}, "b"]]`)
	checkResponses(t, "one call, two responses",
		`[["Test/twice", {}, "a"], ["Core/echo", {}, "b"]]`,
		`[["Test/twice", {"n": 1}, "a"], ["Test/again", {"n": 2}, "a"], ["Core/echo", {}, "b"]]`)
}

func TestCapabilities(t *testing.T) {
	got := process(t, `{"using":["`+CoreCapability+`"],"methodCalls":[["Test/ids", {"ids": []}, "a"]]}`)
	want := `[["error", {"type": "unknownMethod", "description": "no method Test/ids with the capabilities in use"}, "a"]]`
	if !sameJSON(t, got, want) {
		t.Errorf("a method whose capability isn't in use:\n got %s\nwant %s", got, want)
	}

	got = process(t, `{"using":["`+testCapability+`"],"methodCalls":[["Core/echo", {}, "a"], ["Test/ids", {"ids": []}, "b"]]}`)
	want = `[["error", {"type": "unknownMethod", "description": "no method Core/echo with the capabilities in use"}, "a"], ["Test/ids", {"ids": []}, "b"]]`
	if !sameJSON(t, got, want) {
		t.Errorf("a request need not use core:\n got %s\nwant %s", got, want)
	}

	checkRequestError(t, "an unknown capability",
		`{"using":["https://gizmo.example/nope"],"methodCalls":[]}`, ReqErrUnknownCapability, "")

	got = process(t, `{"using":["https://gizmo.example/extra"],"methodCalls":[]}`)
	if want = `[]`; !sameJSON(t, got, want) {
		t.Errorf("a capability with no methods is still known:\n got %s\nwant %s", got, want)
	}
}

func TestRequestErrors(t *testing.T) {
	checkRequestError(t, "not JSON", `{"using":`, ReqErrNotJSON, "")
	checkRequestError(t, "not an object", `[]`, ReqErrNotRequest, "")
	checkRequestError(t, "no methodCalls", `{"using":[]}`, ReqErrNotRequest, "")
	checkRequestError(t, "no using", `{"methodCalls":[]}`, ReqErrNotRequest, "")
	checkRequestError(t, "a call that isn't a triple", `{"using":[],"methodCalls":[["Core/echo",{}]]}`, ReqErrNotRequest, "")
	checkRequestError(t, "arguments that aren't an object", `{"using":[],"methodCalls":[["Core/echo",[],"a"]]}`, ReqErrNotRequest, "")
	checkRequestError(t, "too many calls",
		`{"using":[],"methodCalls":[["Core/echo",{},"a"],["Core/echo",{},"b"],["Core/echo",{},"c"],["Core/echo",{},"d"]]}`,
		ReqErrLimit, "maxCallsInRequest")

	if _, rerr := ParseRequest([]byte(`{"using":[],"methodCalls":[],"future":1}`)); rerr != nil {
		t.Errorf("a member the RFC doesn't define should be ignored, but: %v", rerr)
	}
}

func TestResultReferences(t *testing.T) {
	checkResponses(t, "a reference to an earlier result",
		`[["Core/echo", {"ids": ["a", "b"]}, "0"],
		  ["Test/ids", {"#ids": {"resultOf": "0", "name": "Core/echo", "path": "/ids"}}, "1"]]`,
		`[["Core/echo", {"ids": ["a", "b"]}, "0"], ["Test/ids", {"ids": ["a", "b"]}, "1"]]`)
	checkResponses(t, "a reference through a wildcard",
		`[["Core/echo", {"list": [{"id": "007"}, {"id": "123456789012"}, {"id": "12e4"}]}, "0"],
		  ["Test/ids", {"#ids": {"resultOf": "0", "name": "Core/echo", "path": "/list/*/id"}}, "1"]]`,
		`[["Core/echo", {"list": [{"id": "007"}, {"id": "123456789012"}, {"id": "12e4"}]}, "0"],
		  ["Test/ids", {"ids": ["007", "123456789012", "12e4"]}, "1"]]`)
	checkResponses(t, "a reference to the second response of a call",
		`[["Test/twice", {}, "0"],
		  ["Core/echo", {"#n": {"resultOf": "0", "name": "Test/again", "path": "/n"}}, "1"]]`,
		`[["Test/twice", {"n": 1}, "0"], ["Test/again", {"n": 2}, "0"], ["Core/echo", {"n": 2}, "1"]]`)
	checkResponses(t, "a reference to a call that failed",
		`[["Test/fail", {}, "0"],
		  ["Test/ids", {"#ids": {"resultOf": "0", "name": "Test/fail", "path": "/ids"}}, "1"]]`,
		`[["error", {"type": "forbidden", "description": "no"}, "0"],
		  ["error", {"type": "invalidResultReference", "description": "no Test/fail response with call id \"0\""}, "1"]]`)
	checkResponses(t, "a reference to nothing at the path",
		`[["Core/echo", {}, "0"],
		  ["Test/ids", {"#ids": {"resultOf": "0", "name": "Core/echo", "path": "/ids"}}, "1"]]`,
		`[["Core/echo", {}, "0"],
		  ["error", {"type": "invalidResultReference", "description": "/ids: no property \"ids\""}, "1"]]`)
	checkResponses(t, "an argument given both ways",
		`[["Core/echo", {"ids": []}, "0"],
		  ["Test/ids", {"ids": [], "#ids": {"resultOf": "0", "name": "Core/echo", "path": "/ids"}}, "1"]]`,
		`[["Core/echo", {"ids": []}, "0"],
		  ["error", {"type": "invalidArguments", "description": "both ids and #ids given"}, "1"]]`)
}

// checkCreatedIDs runs a request and compares the createdIds of the response,
// "" meaning that it has none.
func checkCreatedIDs(t *testing.T, name, request, want string) {
	t.Helper()
	req, rerr := ParseRequest([]byte(request))
	if rerr != nil {
		t.Fatal(rerr)
	}
	resp, rerr := testProcessor().Process(context.Background(), req)
	if rerr != nil {
		t.Fatal(rerr)
	}
	got := ""
	if resp.CreatedIDs != nil {
		b, _ := json.Marshal(resp.CreatedIDs)
		got = string(b)
	}
	if got != want && (got == "" || want == "" || !sameJSON(t, got, want)) {
		t.Errorf("%s: got createdIds %q, want %q", name, got, want)
	}
}

func TestCreationIDs(t *testing.T) {
	checkResponses(t, "an id made by one call, used by the next",
		`[["Test/make", {"create": {"k": {"id": "0042"}}}, "0"], ["Test/ids", {"ids": ["#k", "plain"]}, "1"]]`,
		`[["Test/make", {"created": {"k": {"id": "0042"}}}, "0"], ["Test/ids", {"ids": ["0042", "plain"]}, "1"]]`)
	checkResponses(t, "a creation id whose object was unmade",
		`[["Test/make", {"create": {"k": {"id": "regret"}}}, "0"], ["Test/ids", {"ids": ["#k"]}, "1"]]`,
		`[["Test/make", {"created": {}}, "0"],
		  ["error", {"type": "invalidArguments", "description": "nothing was created as #k"}, "1"]]`)
	checkResponses(t, "a creation id nothing made",
		`[["Test/ids", {"ids": ["#k"]}, "0"]]`,
		`[["error", {"type": "invalidArguments", "description": "nothing was created as #k"}, "0"]]`)

	using := `"using":["` + testCapability + `"]`
	checkCreatedIDs(t, "createdIds isn't returned unless it was sent",
		`{`+using+`,"methodCalls":[["Test/make", {"create": {"k": {"id": "x1"}}}, "0"]]}`, "")
	checkCreatedIDs(t, "createdIds is returned, with what was made, when sent",
		`{`+using+`,"createdIds":{"old":"x0"},"methodCalls":[["Test/make", {"create": {"k": {"id": "x1"}}}, "0"]]}`,
		`{"old":"x0","k":"x1"}`)

	got := process(t, `{`+using+`,"createdIds":{"old":"x0"},"methodCalls":[["Test/ids", {"ids": ["#old"]}, "0"]]}`)
	if want := `[["Test/ids", {"ids": ["x0"]}, "0"]]`; !sameJSON(t, got, want) {
		t.Errorf("an id the client sent in createdIds:\n got %s\nwant %s", got, want)
	}
}

func TestRequestContext(t *testing.T) {
	type whoami struct {
		Request string `json:"request"`
		Extra   bool   `json:"extra"`
	}
	ask := func(using string) []whoami {
		t.Helper()
		var responses []Invocation
		out := process(t, `{"using":[`+using+`],"methodCalls":[["Test/whoami", {}, "a"], ["Test/whoami", {}, "b"]]}`)
		if err := json.Unmarshal([]byte(out), &responses); err != nil {
			t.Fatal(err)
		}
		var got []whoami
		for _, r := range responses {
			var w whoami
			if err := json.Unmarshal(r.Args, &w); err != nil {
				t.Fatal(err)
			}
			got = append(got, w)
		}
		return got
	}

	first := ask(`"` + testCapability + `"`)
	second := ask(`"` + testCapability + `","https://gizmo.example/extra"`)
	if first[0].Request == "" || first[0].Request != first[1].Request {
		t.Errorf("calls in one request should share a request id, but got %q and %q", first[0].Request, first[1].Request)
	}
	if first[0].Request == second[0].Request {
		t.Errorf("two requests got the same id, %q", first[0].Request)
	}
	if first[0].Extra || !second[0].Extra {
		t.Errorf("Using: got %v without the capability and %v with it", first[0].Extra, second[0].Extra)
	}
}
