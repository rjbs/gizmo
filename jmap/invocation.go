// Package jmap implements the parts of JMAP (RFC 8620) that don't depend on
// any data type or on HTTP: the request and response envelope, errors, method
// registration and dispatch, result references, creation ids, JSON Pointer and
// PatchObject.
package jmap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// CoreCapability is the capability every JMAP server has.
const CoreCapability = "urn:ietf:params:jmap:core"

// Invocation is a method call or a method response: a three-element array of
// name, arguments and call id.  The arguments are kept as raw JSON, so that
// each method can decode them as suits it.
type Invocation struct {
	Name   string
	Args   json.RawMessage
	CallID string
}

func (inv Invocation) MarshalJSON() ([]byte, error) {
	args := inv.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return json.Marshal([]any{inv.Name, args, inv.CallID})
}

func (inv *Invocation) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil || len(parts) != 3 {
		return errors.New("an invocation is a three-element array")
	}
	if err := json.Unmarshal(parts[0], &inv.Name); err != nil {
		return errors.New("an invocation's name must be a string")
	}
	if len(parts[1]) == 0 || parts[1][0] != '{' {
		return errors.New("an invocation's arguments must be an object")
	}
	if err := json.Unmarshal(parts[2], &inv.CallID); err != nil {
		return errors.New("an invocation's call id must be a string")
	}
	inv.Args = parts[1]
	return nil
}

// Request is the JMAP Request object (RFC 8620 §3.3).
type Request struct {
	Using       []string          `json:"using"`
	MethodCalls []Invocation      `json:"methodCalls"`
	CreatedIDs  map[string]string `json:"createdIds,omitempty"`
}

// Response is the JMAP Response object (RFC 8620 §3.4).
type Response struct {
	MethodResponses []Invocation      `json:"methodResponses"`
	CreatedIDs      map[string]string `json:"createdIds,omitempty"`
	SessionState    string            `json:"sessionState"`
}

// ParseRequest decodes the body of an API request.  A body that isn't JSON is
// a notJSON error, and JSON that isn't a Request object is notRequest.
// Members of the object that RFC 8620 doesn't define are ignored.
func ParseRequest(body []byte) (*Request, *RequestError) {
	if !json.Valid(body) {
		return nil, &RequestError{Type: ReqErrNotJSON, Status: 400, Detail: "the request body is not valid JSON"}
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, &RequestError{Type: ReqErrNotRequest, Status: 400, Detail: "the request is not a JMAP Request object: " + err.Error()}
	}
	if req.Using == nil || req.MethodCalls == nil {
		return nil, &RequestError{Type: ReqErrNotRequest, Status: 400, Detail: "a JMAP Request object has using and methodCalls"}
	}
	return &req, nil
}

// DecodeArgs decodes a method's arguments into a struct, and reports any
// argument the struct has no field for as invalidArguments.
func DecodeArgs(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return ArgError("%v", err)
	}
	return nil
}

// DecodeValue decodes arbitrary JSON into Go values, keeping numbers as
// json.Number so that values stored and later encoded again come back exactly
// as they were sent.
func DecodeValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

// DecodeObject is DecodeValue restricted to a JSON object.
func DecodeObject(data []byte) (map[string]any, error) {
	v, err := DecodeValue(data)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object")
	}
	return m, nil
}

// AccountID picks the accountId out of a method's arguments.  A call that
// names no account, or names one with something other than a string, is
// invalidArguments.
func AccountID(args json.RawMessage) (string, error) {
	var a struct {
		AccountID json.RawMessage `json:"accountId"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", ArgError("arguments must be an object")
	}
	if a.AccountID == nil {
		return "", ArgError("accountId is required")
	}
	var id string
	if err := json.Unmarshal(a.AccountID, &id); err != nil {
		return "", ArgError("accountId must be a string")
	}
	return id, nil
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

// ValidID reports whether s is a syntactically valid JMAP Id (RFC 8620 §1.2).
// The RFC advises against ids that begin with a digit or are all digits, but
// allows them, and so does this.
func ValidID(s string) bool {
	return idPattern.MatchString(s)
}
