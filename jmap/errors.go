package jmap

import "fmt"

// MethodError is a method-level error (RFC 8620 §3.6.2).  A method returns
// one to have its response replaced by an "error" response.
type MethodError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

func (e *MethodError) Error() string {
	if e.Description == "" {
		return e.Type
	}
	return e.Type + ": " + e.Description
}

// Errorf builds a [MethodError] with a formatted description.
func Errorf(typ, format string, args ...any) *MethodError {
	return &MethodError{Type: typ, Description: fmt.Sprintf(format, args...)}
}

// ArgError builds an invalidArguments [MethodError].
func ArgError(format string, args ...any) *MethodError {
	return Errorf(ErrInvalidArguments, format, args...)
}

// The method-level error types of RFC 8620 §3.6.2 and §5.
const (
	ErrServerFail             = "serverFail"
	ErrUnknownMethod          = "unknownMethod"
	ErrInvalidArguments       = "invalidArguments"
	ErrInvalidResultReference = "invalidResultReference"
	ErrForbidden              = "forbidden"
	ErrAccountNotFound        = "accountNotFound"
	ErrAccountNotSupported    = "accountNotSupportedByMethod"
	ErrAccountReadOnly        = "accountReadOnly"
	ErrRequestTooLarge        = "requestTooLarge"
	ErrStateMismatch          = "stateMismatch"
	ErrCannotCalculateChanges = "cannotCalculateChanges"
	ErrUnsupportedFilter      = "unsupportedFilter"
	ErrUnsupportedSort        = "unsupportedSort"
	ErrAnchorNotFound         = "anchorNotFound"
)

// SetError is the error for one object in a /set response (RFC 8620 §5.3).
type SetError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
	ExistingID  string   `json:"existingId,omitempty"`
}

func (e *SetError) Error() string {
	if e.Description == "" {
		return e.Type
	}
	return e.Type + ": " + e.Description
}

// The [SetError] types of RFC 8620 §5.3 and §5.4.
const (
	SetErrForbidden         = "forbidden"
	SetErrOverQuota         = "overQuota"
	SetErrTooLarge          = "tooLarge"
	SetErrRateLimit         = "rateLimit"
	SetErrNotFound          = "notFound"
	SetErrInvalidPatch      = "invalidPatch"
	SetErrWillDestroy       = "willDestroy"
	SetErrInvalidProperties = "invalidProperties"
	SetErrSingleton         = "singleton"
	SetErrAlreadyExists     = "alreadyExists"
)

// InvalidProperties builds an invalidProperties [SetError] naming the
// properties at fault.
func InvalidProperties(desc string, props ...string) *SetError {
	return &SetError{Type: SetErrInvalidProperties, Description: desc, Properties: props}
}

// AlreadyExists is the [SetError] for a create that duplicates an existing
// record.  It carries the id of the record that already exists.
func AlreadyExists(desc, existingID string) *SetError {
	return &SetError{Type: SetErrAlreadyExists, Description: desc, ExistingID: existingID}
}

// NotFound is the [SetError] for an unknown id.
func NotFound() *SetError {
	return &SetError{Type: SetErrNotFound}
}

// RequestError is a request-level error, sent as an RFC 7807 problem details
// object (RFC 8620 §3.6.1).
type RequestError struct {
	Type   string `json:"type"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	Limit  string `json:"limit,omitempty"`
}

func (e *RequestError) Error() string {
	return e.Type + ": " + e.Detail
}

// The request-level error types of RFC 8620 §3.6.1.
const (
	ReqErrUnknownCapability = "urn:ietf:params:jmap:error:unknownCapability"
	ReqErrNotJSON           = "urn:ietf:params:jmap:error:notJSON"
	ReqErrNotRequest        = "urn:ietf:params:jmap:error:notRequest"
	ReqErrLimit             = "urn:ietf:params:jmap:error:limit"
)

// LimitError is the request-level error for exceeding the named limit.
func LimitError(limit, detail string) *RequestError {
	return &RequestError{Type: ReqErrLimit, Status: 400, Limit: limit, Detail: detail}
}
