package jmap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
)

// Handler implements one JMAP method.  It is given the call's arguments, with
// any result references already resolved, and returns the arguments of its
// response, which may be anything that encodes as a JSON object.  Returning a
// *[MethodError] produces an "error" response.  Any other error is logged and
// becomes serverFail, with nothing of the error told to the client.
type Handler func(c *Call, args json.RawMessage) (any, error)

type method struct {
	capability string
	handler    Handler
}

// Processor dispatches the method calls of a [Request].
type Processor struct {
	methods      map[string]method
	capabilities map[string]bool

	// MaxCallsInRequest is the most method calls one request may make.
	MaxCallsInRequest int

	// SessionState gives the session state to report with each response.
	SessionState func(ctx context.Context) string

	// Logger is told of errors that become serverFail.
	Logger *slog.Logger
}

// NewProcessor returns a [Processor] that knows only the core capability and
// its one method, Core/echo.
func NewProcessor() *Processor {
	p := &Processor{
		methods:           map[string]method{},
		capabilities:      map[string]bool{CoreCapability: true},
		MaxCallsInRequest: 64,
		SessionState:      func(context.Context) string { return "0" },
		Logger:            slog.Default(),
	}
	p.Register("Core/echo", CoreCapability, func(_ *Call, args json.RawMessage) (any, error) {
		return args, nil
	})
	return p
}

// Register adds a method.  It can be called only by a request whose "using"
// includes the capability.
func (p *Processor) Register(name, capability string, h Handler) {
	p.methods[name] = method{capability: capability, handler: h}
	p.capabilities[capability] = true
}

// AddCapability makes a capability known that has no methods of its own, such
// as one that only changes how other methods behave.
func (p *Processor) AddCapability(capability string) {
	p.capabilities[capability] = true
}

// Capabilities lists the capabilities the processor knows, in no order.
func (p *Processor) Capabilities() []string {
	out := make([]string, 0, len(p.capabilities))
	for c := range p.capabilities {
		out = append(out, c)
	}
	return out
}

// request is the state shared by every call in one request.
type request struct {
	id         string
	using      map[string]bool
	createdIDs map[string]string
	responses  []Invocation
}

// Call is one method call being handled.  It gives the method what it may
// need of the request around it.
type Call struct {
	// Name is the method being called, and ID the client's id for the call.
	Name string
	ID   string

	ctx   context.Context
	req   *request
	extra []Invocation
	err   error
}

// Context returns the call's context.  It carries the request's id and
// capabilities, for [RequestID] and [Using], as well as whatever the HTTP
// layer put there, such as the caller's identity.
func (c *Call) Context() context.Context { return c.ctx }

// CreatedID returns the id recorded for a creation id earlier in the request,
// or sent by the client in the request's createdIds.
func (c *Call) CreatedID(creationID string) (string, bool) {
	id, ok := c.req.createdIDs[creationID]
	return id, ok
}

// SetCreatedID records the id of an object created under a creation id, so
// that later calls in the request can refer to it as "#" and the creation id.
func (c *Call) SetCreatedID(creationID, id string) {
	c.req.createdIDs[creationID] = id
}

// ForgetCreatedID takes back a SetCreatedID, for a method that undoes a
// creation it had made before it finishes.
func (c *Call) ForgetCreatedID(creationID string) {
	delete(c.req.createdIDs, creationID)
}

// ResolveID maps a creation reference, "#" and a creation id, to the id it
// stands for.  Any other string is returned as it is.  The boolean is false
// only for a creation reference that nothing in the request has created.
func (c *Call) ResolveID(id string) (string, bool) {
	if !strings.HasPrefix(id, "#") {
		return id, true
	}
	return c.CreatedID(id[1:])
}

// Respond adds a further response to the call, after the one the handler
// returns.  This is for methods that run another as a consequence, as /copy
// does with /set to destroy the originals.
func (c *Call) Respond(name string, args any) {
	raw, err := json.Marshal(args)
	if err != nil {
		c.err = err
		return
	}
	c.extra = append(c.extra, Invocation{Name: name, Args: raw, CallID: c.ID})
}

type requestIDKey struct{}
type usingKey struct{}

// RequestID returns the id of the API request being processed, or "" outside
// of one.  The id is the same for every call in a request and different for
// every request.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// Using reports whether the API request being processed is using a
// capability.
func Using(ctx context.Context, capability string) bool {
	using, _ := ctx.Value(usingKey{}).(map[string]bool)
	return using[capability]
}

func newRequestID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Process runs each method call in req, in order, and returns the [Response],
// or a [RequestError] if the request as a whole can't be accepted.
func (p *Processor) Process(ctx context.Context, req *Request) (*Response, *RequestError) {
	using := map[string]bool{}
	for _, u := range req.Using {
		if !p.capabilities[u] {
			return nil, &RequestError{Type: ReqErrUnknownCapability, Status: 400, Detail: "unknown capability " + u}
		}
		using[u] = true
	}
	if len(req.MethodCalls) > p.MaxCallsInRequest {
		return nil, LimitError("maxCallsInRequest", "too many method calls")
	}

	r := &request{id: newRequestID(), using: using, createdIDs: map[string]string{}, responses: []Invocation{}}
	for k, v := range req.CreatedIDs {
		r.createdIDs[k] = v
	}
	ctx = context.WithValue(ctx, requestIDKey{}, r.id)
	ctx = context.WithValue(ctx, usingKey{}, using)

	for _, call := range req.MethodCalls {
		r.responses = append(r.responses, p.invoke(ctx, r, call)...)
	}

	resp := &Response{MethodResponses: r.responses, SessionState: p.SessionState(ctx)}
	// RFC 8620 §3.4: createdIds is returned only if the request gave one.
	if req.CreatedIDs != nil {
		resp.CreatedIDs = r.createdIDs
	}
	return resp, nil
}

func (p *Processor) invoke(ctx context.Context, r *request, call Invocation) []Invocation {
	fail := func(err error) []Invocation {
		var me *MethodError
		if !errors.As(err, &me) {
			p.Logger.ErrorContext(ctx, "method failed", "method", call.Name, "request", r.id, "error", err)
			me = Errorf(ErrServerFail, "internal error")
		}
		raw, _ := json.Marshal(me)
		return []Invocation{{Name: "error", Args: raw, CallID: call.CallID}}
	}

	m, ok := p.methods[call.Name]
	if !ok || !r.using[m.capability] {
		return fail(Errorf(ErrUnknownMethod, "no method %s with the capabilities in use", call.Name))
	}
	args, err := resolveReferences(call.Args, r.responses)
	if err != nil {
		return fail(err)
	}

	c := &Call{Name: call.Name, ID: call.CallID, ctx: ctx, req: r}
	result, err := m.handler(c, args)
	if err == nil {
		err = c.err
	}
	if err != nil {
		return fail(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fail(err)
	}
	return append([]Invocation{{Name: call.Name, Args: raw, CallID: call.CallID}}, c.extra...)
}

// resolveReferences replaces each "#name" argument, a ResultReference, with
// the value it points at in an earlier response (RFC 8620 §3.7).
func resolveReferences(raw json.RawMessage, prior []Invocation) (json.RawMessage, error) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, ArgError("arguments must be an object")
	}
	changed := false
	for key, val := range args {
		if !strings.HasPrefix(key, "#") {
			continue
		}
		name := key[1:]
		if _, both := args[name]; both {
			return nil, ArgError("both %s and #%s given", name, name)
		}
		var ref struct {
			ResultOf string `json:"resultOf"`
			Name     string `json:"name"`
			Path     string `json:"path"`
		}
		if err := json.Unmarshal(val, &ref); err != nil {
			return nil, Errorf(ErrInvalidResultReference, "malformed reference in #%s", name)
		}
		var source *Invocation
		for i := range prior {
			if prior[i].CallID == ref.ResultOf && prior[i].Name == ref.Name {
				source = &prior[i]
				break
			}
		}
		if source == nil {
			return nil, Errorf(ErrInvalidResultReference, "no %s response with call id %q", ref.Name, ref.ResultOf)
		}
		doc, err := DecodeValue(source.Args)
		if err != nil {
			return nil, err
		}
		value, err := ResolvePointer(doc, ref.Path)
		if err != nil {
			return nil, Errorf(ErrInvalidResultReference, "%s: %v", ref.Path, err)
		}
		b, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		delete(args, key)
		args[name] = b
		changed = true
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(args)
}
