// Package jmaptest is a JMAP client built for testing servers, after the
// Perl library JMAP::Tester, whose vocabulary it keeps.
//
// A sentence is one method response.  A paragraph is the run of sentences
// that answer one method call, which is usually a single sentence.  Asking a
// response for something it doesn't have, such as its single sentence when
// it has two, fails the test at once, so a test can say what it expects by
// what it asks for.
package jmaptest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Tester is a JMAP client for one server, as one caller.
type Tester struct {
	T testing.TB

	// BaseURL is the server's address, with no trailing slash.
	BaseURL string

	// Using is the capabilities each request uses.
	Using []string

	// AccountID, if set, is given as the accountId of every method call
	// that doesn't give one.
	AccountID string

	// Authorization, if set, is sent as each request's Authorization
	// header.
	Authorization string

	HTTP *http.Client
}

// New returns a Tester for the server at baseURL.
func New(t testing.TB, baseURL string, using ...string) *Tester {
	return &Tester{T: t, BaseURL: strings.TrimSuffix(baseURL, "/"), Using: using, HTTP: http.DefaultClient}
}

// With returns a copy of the tester, changed by fn.  It is for making a
// tester that differs in one respect, such as its account.
func (c *Tester) With(fn func(*Tester)) *Tester {
	d := *c
	d.Using = append([]string(nil), c.Using...)
	fn(&d)
	return &d
}

// Args are the arguments of a method call.
type Args = map[string]any

// omit is the type of Omit.
type omit struct{}

// Omit, as the value of an argument, leaves the argument out of the call.
// Its use is to make a call with no accountId from a tester that has one.
var Omit = omit{}

// Call is a method call to make.
type Call struct {
	Name string
	Args Args

	// ID is the method call id.  If empty, the call's position in the
	// request is used: "0", "1", and so on.
	ID string
}

// Do makes a Call.
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

func (c *Tester) do(method, path, contentType string, body []byte) (*http.Response, []byte) {
	c.T.Helper()
	return c.send(method, path, contentType, bytes.NewReader(body), nil)
}

// send makes a request whose body is a stream, with any further headers.
func (c *Tester) send(method, path, contentType string, body io.Reader, header map[string]string) (*http.Response, []byte) {
	c.T.Helper()
	req, err := http.NewRequest(method, c.BaseURL+path, body)
	if err != nil {
		c.T.Fatalf("%s %s: %v", method, path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, value := range header {
		req.Header.Set(name, value)
	}
	if c.Authorization != "" {
		req.Header.Set("Authorization", c.Authorization)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.T.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.T.Fatalf("%s %s: reading the response: %v", method, path, err)
	}
	return resp, data
}

// Session fetches the session object.
func (c *Tester) Session() map[string]any {
	c.T.Helper()
	resp, body := c.do("GET", "/.well-known/jmap", "", nil)
	if resp.StatusCode != 200 {
		c.T.Fatalf("fetching the session: status %d: %s", resp.StatusCode, body)
	}
	return decodeObject(c.T, body)
}

// Request makes an API request of the given calls, and fails the test unless
// the server answers it with a JMAP response.  Method-level errors are part
// of such a response; they are for the test to look for.
func (c *Tester) Request(calls ...Call) *Response {
	c.T.Helper()
	triples := make([]any, len(calls))
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
		triples[i] = []any{call.Name, args, id}
	}
	using := c.Using
	if using == nil {
		using = []string{}
	}
	body, err := json.Marshal(map[string]any{"using": using, "methodCalls": triples})
	if err != nil {
		c.T.Fatalf("encoding the request: %v", err)
	}
	raw := c.Post(string(body))
	if raw.Status != 200 {
		c.T.Fatalf("the request was refused: status %d: %s\nrequest: %s", raw.Status, raw.Body, body)
	}
	return parseResponse(c.T, raw.Body)
}

// Posted is the plain result of posting something to the API endpoint.
type Posted struct {
	Status      int
	ContentType string
	Body        []byte
}

// Post sends a body to the API endpoint just as given.  It is for requests
// that Request can't or shouldn't make: malformed ones, and ones with
// members of their own, like createdIds.
func (c *Tester) Post(body string) *Posted {
	c.T.Helper()
	resp, data := c.do("POST", "/jmap/api", "application/json", []byte(body))
	return &Posted{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: data}
}

// Response reads what was posted as a JMAP response, failing the test if it
// isn't one.
func (p *Posted) Response(t testing.TB) *Response {
	t.Helper()
	if p.Status != 200 {
		t.Fatalf("the request was refused: status %d: %s", p.Status, p.Body)
	}
	return parseResponse(t, p.Body)
}

// AssertRequestError fails the test unless the request was refused as a
// whole, with a problem document of the given type, which may be given
// without its "urn:ietf:params:jmap:error:" prefix.  It returns the problem
// document.
func (p *Posted) AssertRequestError(t testing.TB, typ string) map[string]any {
	t.Helper()
	if !strings.Contains(typ, ":") {
		typ = "urn:ietf:params:jmap:error:" + typ
	}
	if p.Status != 400 || p.ContentType != "application/problem+json" {
		t.Fatalf("expected the request to be refused as %s, but got status %d, %s: %s", typ, p.Status, p.ContentType, p.Body)
	}
	problem := decodeObject(t, p.Body)
	if problem["type"] != typ {
		t.Fatalf("expected the request to be refused as %s, but got %s", typ, p.Body)
	}
	return problem
}

// Uploaded is the result of an upload.
type Uploaded struct {
	Status int
	Body   []byte

	// BlobID, Type and Size are from the server's answer, when the upload
	// was accepted.
	BlobID string
	Type   string
	Size   int64
}

// Upload sends data to the upload endpoint of the tester's account.
func (c *Tester) Upload(contentType string, data []byte) *Uploaded {
	c.T.Helper()
	return c.uploaded(c.do("POST", "/jmap/upload/"+c.AccountID+"/", contentType, data))
}

// UploadStream sends a stream to the upload endpoint of the tester's
// account, without saying beforehand how long it is.
func (c *Tester) UploadStream(contentType string, data io.Reader) *Uploaded {
	c.T.Helper()
	// A reader that is only a reader keeps the client from learning the
	// length, so the upload is sent in chunks.
	return c.uploaded(c.send("POST", "/jmap/upload/"+c.AccountID+"/", contentType, struct{ io.Reader }{data}, nil))
}

func (c *Tester) uploaded(resp *http.Response, body []byte) *Uploaded {
	c.T.Helper()
	up := &Uploaded{Status: resp.StatusCode, Body: body}
	if resp.StatusCode == 201 {
		var answer struct {
			BlobID string `json:"blobId"`
			Type   string `json:"type"`
			Size   int64  `json:"size"`
		}
		if err := json.Unmarshal(body, &answer); err != nil {
			c.T.Fatalf("the upload's answer isn't what it should be: %v: %s", err, body)
		}
		up.BlobID, up.Type, up.Size = answer.BlobID, answer.Type, answer.Size
	}
	return up
}

// Downloaded is the result of a download.
type Downloaded struct {
	Status int
	Header http.Header
	Body   []byte
}

// Download fetches a blob from the tester's account, asking for it under the
// given name and, unless it is empty, with the given content type.
func (c *Tester) Download(blobID, name, contentType string) *Downloaded {
	c.T.Helper()
	return c.DownloadRange(blobID, name, contentType, "")
}

// DownloadRange is Download for part of a blob, given as the value of a
// Range header, such as "bytes=0-99".
func (c *Tester) DownloadRange(blobID, name, contentType, byteRange string) *Downloaded {
	c.T.Helper()
	path := "/jmap/download/" + c.AccountID + "/" + blobID + "/" + name
	if contentType != "" {
		path += "?type=" + contentType
	}
	header := map[string]string{}
	if byteRange != "" {
		header["Range"] = byteRange
	}
	resp, body := c.send("GET", path, "", http.NoBody, header)
	return &Downloaded{Status: resp.StatusCode, Header: resp.Header, Body: body}
}

func decodeObject(t testing.TB, data []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("expected a JSON object, but: %v: %s", err, data)
	}
	return m
}
