// Package client is a JMAP client, for programs that talk to a server:
// importers, command-line tools, and the like.  It knows the protocol and
// nothing of any data type.
//
// A [Client] finds the server's session, makes API requests of one or more
// method calls, and uploads and downloads blobs.  What a server refuses
// comes back as an error: a *[RequestError] when the request as a whole was
// refused, and a *[MethodError] when one method call was.
//
// For testing a server, see the jmaptest package, which fails a test where
// this package returns an error.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// Client is a JMAP client of one server, as one caller.  Its fields are to
// be set before it is first used, and left alone after; it may then be used
// from many goroutines at once.
type Client struct {
	// BaseURL is where the server is, like "https://tasks.example.com".
	// The session is looked for at /.well-known/jmap under it.
	BaseURL string

	// SessionURL, if set, is where the session is, when that isn't the
	// well-known place.  It may be relative to BaseURL.
	SessionURL string

	// Authorization, if set, is sent as each request's Authorization
	// header, like "Bearer " followed by a token.
	Authorization string

	// Using is the capabilities each API request uses.
	Using []string

	// AccountID, if set, is given as the accountId of every method call
	// that doesn't give one, and is the account that blobs are uploaded
	// to and downloaded from.  UseAccount sets it from the session.
	AccountID string

	// HTTP makes the requests.  If nil, http.DefaultClient does.
	HTTP *http.Client

	mu      sync.Mutex
	session *Session
}

// New returns a [Client] of the server at baseURL, whose requests use the
// given capabilities.
func New(baseURL string, using ...string) *Client {
	return &Client{BaseURL: baseURL, Using: using}
}

// Session is a server's session object (RFC 8620 §2), in the parts a client
// acts on.  The URLs are absolute, whatever the server gave.
type Session struct {
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
	Accounts        map[string]Account         `json:"accounts"`
	PrimaryAccounts map[string]string          `json:"primaryAccounts"`
	Username        string                     `json:"username"`
	APIURL          string                     `json:"apiUrl"`
	DownloadURL     string                     `json:"downloadUrl"`
	UploadURL       string                     `json:"uploadUrl"`
	EventSourceURL  string                     `json:"eventSourceUrl"`
	State           string                     `json:"state"`
}

// Account is one account of a session.
type Account struct {
	Name                string                     `json:"name"`
	IsPersonal          bool                       `json:"isPersonal"`
	IsReadOnly          bool                       `json:"isReadOnly"`
	AccountCapabilities map[string]json.RawMessage `json:"accountCapabilities"`
}

// RequestError is a request that the server refused as a whole, or
// answered with something other than success.
type RequestError struct {
	// Status is the HTTP status.
	Status int

	// Type and Detail are from the problem document (RFC 7807) that the
	// server sent, if it sent one.  Type is like
	// "urn:ietf:params:jmap:error:notRequest".
	Type, Detail string

	// Body is the start of what the server sent.
	Body string
}

func (e *RequestError) Error() string {
	what := "status " + strconv.Itoa(e.Status)
	switch {
	case e.Type != "" && e.Detail != "":
		return what + ": " + e.Type + ": " + e.Detail
	case e.Type != "":
		return what + ": " + e.Type
	case e.Body != "":
		return what + ": " + e.Body
	}
	return what
}

// send makes a request and returns its answer, which the caller closes.
// An answer that isn't a success is returned as a *RequestError.
func (c *Client) send(ctx context.Context, method, target, contentType string, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Authorization != "" {
		req.Header.Set("Authorization", c.Authorization)
	}
	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	res, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 == 2 {
		return res, nil
	}
	defer res.Body.Close()
	said, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
	failed := &RequestError{Status: res.StatusCode, Body: strings.TrimSpace(string(said))}
	var problem struct{ Type, Detail string }
	if json.Unmarshal(said, &problem) == nil {
		failed.Type, failed.Detail = problem.Type, problem.Detail
	}
	return nil, failed
}

// sendJSON makes a request and decodes its answer into out.
func (c *Client) sendJSON(ctx context.Context, method, target, contentType string, body io.Reader, size int64, out any) error {
	res, err := c.send(ctx, method, target, contentType, body, size)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("the server's answer isn't the JSON it should be: %w", err)
	}
	return nil
}

// Session returns the server's session, fetching it if this client hasn't
// yet.  It is not fetched again until Refresh is called.
func (c *Client) Session(ctx context.Context) (*Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		return c.session, nil
	}
	return c.fetchSession(ctx)
}

// Refresh fetches the session anew.  It is for when a Response's
// SessionState shows that the session has changed.
func (c *Client) Refresh(ctx context.Context) (*Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetchSession(ctx)
}

func (c *Client) fetchSession(ctx context.Context) (*Session, error) {
	base, err := url.Parse(strings.TrimSuffix(c.BaseURL, "/") + "/")
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("%q is not the URL of a server", c.BaseURL)
	}
	at := ".well-known/jmap"
	if c.SessionURL != "" {
		at = c.SessionURL
	}
	where, err := base.Parse(at)
	if err != nil {
		return nil, fmt.Errorf("%q is not the URL of a session: %w", c.SessionURL, err)
	}

	s := &Session{}
	if err := c.sendJSON(ctx, "GET", where.String(), "", nil, -1, s); err != nil {
		return nil, fmt.Errorf("fetching the session from %s: %w", where, err)
	}
	if s.APIURL == "" {
		return nil, fmt.Errorf("what is at %s is not a JMAP session", where)
	}
	for _, u := range []*string{&s.APIURL, &s.DownloadURL, &s.UploadURL, &s.EventSourceURL} {
		// The URLs are templates, whose braces a URL parser would
		// escape, so a relative one is resolved by hand.
		switch {
		case *u == "" || strings.Contains(*u, "://"):
		case strings.HasPrefix(*u, "/"):
			*u = where.Scheme + "://" + where.Host + *u
		default:
			*u = where.Scheme + "://" + where.Host + where.Path[:strings.LastIndex(where.Path, "/")+1] + *u
		}
	}
	c.session = s
	return s, nil
}

// UseAccount sets AccountID to the caller's primary account for a
// capability, from the session.  It is to be called before the client is
// put to use by more than one goroutine.
func (c *Client) UseAccount(ctx context.Context, capability string) error {
	s, err := c.Session(ctx)
	if err != nil {
		return err
	}
	id := s.PrimaryAccounts[capability]
	if id == "" {
		return fmt.Errorf("the server has no primary account for %s", capability)
	}
	c.AccountID = id
	return nil
}

// Args are the arguments of a method call.
type Args = map[string]any

// omit is the type of Omit.
type omit struct{}

// Omit, as the value of an argument, leaves the argument out of the call.
// Its use is to make a call with no accountId from a client that has one.
var Omit = omit{}

// Call is a method call to make.
type Call struct {
	Name string
	Args Args

	// ID is the method call id.  If empty, the call's position in the
	// request is used: "0", "1", and so on.
	ID string
}

// Do makes a [Call].
func Do(name string, args Args) Call {
	return Call{Name: name, Args: args}
}

// As gives the call a method call id of its own.
func (c Call) As(id string) Call {
	c.ID = id
	return c
}

// Ref is a result reference (RFC 8620 §3.7), for the value of an argument
// whose name begins with "#".
func Ref(resultOf, name, path string) Args {
	return Args{"resultOf": resultOf, "name": name, "path": path}
}

// MethodError is a method call that the server answered with an error: the
// method wasn't done at all.
type MethodError struct {
	// Method is the name of the method called.
	Method string

	// Type is the kind of error, like "invalidArguments".
	Type string

	Description string

	// Args is the whole of the error's arguments.
	Args json.RawMessage
}

func (e *MethodError) Error() string {
	if e.Description == "" {
		return e.Method + ": " + e.Type
	}
	return e.Method + ": " + e.Type + ": " + e.Description
}

// IsMethodError reports whether err is a method-level error of the given
// type, like "stateMismatch".
func IsMethodError(err error, typ string) bool {
	var failed *MethodError
	return errors.As(err, &failed) && failed.Type == typ
}

// Result is one method response.
type Result struct {
	// Name is the response's name: the method's, or "error".
	Name string

	// Args is the response's arguments.
	Args json.RawMessage

	// CallID is the id of the method call that this answers.
	CallID string

	// method is the method called, when that can be told.
	method string
}

// Err returns the *[MethodError] that the result is, or nil if it is not an
// error.
func (r *Result) Err() error {
	if r.Name != "error" {
		return nil
	}
	failed := &MethodError{Method: r.method, Args: r.Args}
	var e struct{ Type, Description string }
	json.Unmarshal(r.Args, &e)
	failed.Type, failed.Description = e.Type, e.Description
	return failed
}

// Decode decodes the result's arguments into out.  If the result is an
// error, it returns that instead.
func (r *Result) Decode(out any) error {
	if err := r.Err(); err != nil {
		return err
	}
	return json.Unmarshal(r.Args, out)
}

// Response is the server's answer to an API request.
type Response struct {
	// Results are the method responses, in order.  There may be more or
	// fewer than there were calls.
	Results []*Result

	// CreatedIDs is the response's createdIds, or nil if it had none.
	CreatedIDs map[string]string

	// SessionState is the state of the session as the server has it.  If
	// it isn't the State of the client's Session, the session has changed.
	SessionState string
}

// For returns the results that answer the method call with the given id.
func (r *Response) For(callID string) []*Result {
	var out []*Result
	for _, res := range r.Results {
		if res.CallID == callID {
			out = append(out, res)
		}
	}
	return out
}

// Err returns the first method-level error among the results, or nil.
func (r *Response) Err() error {
	for _, res := range r.Results {
		if err := res.Err(); err != nil {
			return err
		}
	}
	return nil
}

// Request makes an API request of the given calls.  The error it returns is
// for a request that got no JMAP response.  Method-level errors are part of
// a response, and are found in its Results.
func (c *Client) Request(ctx context.Context, calls ...Call) (*Response, error) {
	s, err := c.Session(ctx)
	if err != nil {
		return nil, err
	}
	triples := make([]any, len(calls))
	methods := map[string]string{}
	for i, call := range calls {
		args := Args{}
		if c.AccountID != "" {
			args["accountId"] = c.AccountID
		}
		for k, v := range call.Args {
			if v == Omit {
				delete(args, k)
			} else {
				args[k] = v
			}
		}
		id := call.ID
		if id == "" {
			id = strconv.Itoa(i)
		}
		methods[id] = call.Name
		triples[i] = []any{call.Name, args, id}
	}
	using := c.Using
	if using == nil {
		using = []string{}
	}
	body, err := json.Marshal(map[string]any{"using": using, "methodCalls": triples})
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	var raw struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
		CreatedIDs      map[string]string   `json:"createdIds"`
		SessionState    string              `json:"sessionState"`
	}
	if err := c.sendJSON(ctx, "POST", s.APIURL, "application/json", bytes.NewReader(body), int64(len(body)), &raw); err != nil {
		return nil, err
	}
	if raw.MethodResponses == nil {
		return nil, errors.New("the server's answer is not a JMAP response")
	}
	resp := &Response{CreatedIDs: raw.CreatedIDs, SessionState: raw.SessionState}
	for _, parts := range raw.MethodResponses {
		res := &Result{}
		if len(parts) != 3 || json.Unmarshal(parts[0], &res.Name) != nil || json.Unmarshal(parts[2], &res.CallID) != nil {
			return nil, errors.New("the server's answer has a method response that isn't one")
		}
		res.Args = parts[1]
		res.method = methods[res.CallID]
		resp.Results = append(resp.Results, res)
	}
	return resp, nil
}

// Call makes a request of one method call, and decodes the arguments of its
// response into out, which may be nil if they aren't wanted.  A method-level
// error is returned as a *[MethodError].
func (c *Client) Call(ctx context.Context, method string, args Args, out any) error {
	resp, err := c.Request(ctx, Do(method, args))
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if len(resp.Results) != 1 {
		return fmt.Errorf("%s: the server gave %d responses to one call", method, len(resp.Results))
	}
	if out == nil {
		return resp.Results[0].Err()
	}
	return resp.Results[0].Decode(out)
}
