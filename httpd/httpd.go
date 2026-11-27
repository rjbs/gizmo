// Package httpd serves JMAP over HTTP: the session resource, the API
// endpoint, the event source, and the blob endpoints.
//
// A [Server] is an [http.Handler] for the application to mount on its own
// mux, beside whatever else it serves.  It answers these paths:
//
//	GET  /.well-known/jmap
//	GET  /jmap/session
//	POST /jmap/api
//	GET  /jmap/eventsource
//	GET  /jmap/download/{accountId}/{blobId}/{name}
//	POST /jmap/upload/{accountId}/
//
// Every request must already carry an authenticated user in its context, put
// there by the auth package's middleware; the Server refuses anything else.
//
// The Server also refuses what its [Guard] does: requests that a browser
// makes for a page from somewhere else, and requests addressed to a name the
// server isn't known by.
package httpd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/rjbs/gizmo/auth"
	"github.com/rjbs/gizmo/blob"
	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/push"
)

// Account is what the [Server] needs to know of one account's data.
type Account interface {
	// Capabilities is the account's accountCapabilities object: each
	// capability the account supports, with that capability's details.
	Capabilities() map[string]any

	// States gives the current state string of each of the account's
	// types, for the event source to report.
	States(ctx context.Context) (map[string]string, error)
}

// Config configures a [Server].
type Config struct {
	// BaseURL, if set, is used to build the URLs in the session object.
	// Otherwise they are worked out from each request.
	BaseURL string

	// Processor handles API requests.  The Server sets its SessionState
	// and its MaxCallsInRequest.
	Processor *jmap.Processor

	// Directory names the accounts.
	Directory *auth.Directory

	// Account returns an account's data, or nil if it has none.
	Account func(accountID string) Account

	// Broker feeds the event source.
	Broker *push.Broker

	// Limits are advertised in the core capability.  The Server keeps to
	// those that are its to keep: the size of a request and of an upload,
	// the calls in a request, and how many requests and uploads one user
	// has under way at once.  A request or upload past that number waits.
	// The limits on objects are for whatever answers /get and /set.
	Limits jmap.Limits

	// Capabilities gives the details of capabilities that have any, for
	// the session's capabilities object.  Every capability the Processor
	// knows is in that object whether it is here or not, with no details
	// unless it is.  This is not the place for core,
	// which the Server supplies.
	Capabilities map[string]any

	// Collations are the collation algorithms advertised.
	Collations []string

	// Blobs keeps the blobs that the upload and download endpoints serve.
	// If nil, there are none, and both endpoints answer 404.
	Blobs blob.Store

	// Guard keeps browsers from being used against the server.  If nil,
	// the Server makes one.  Either way, the Server tells it the name in
	// BaseURL; any other name the server goes by has to be given to it.
	Guard *Guard

	Logger *slog.Logger
}

// Server serves JMAP over HTTP.
type Server struct {
	cfg Config
	mux *http.ServeMux

	guarded http.Handler

	requests, uploads *limiter
}

func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Collations == nil {
		cfg.Collations = []string{}
	}
	if cfg.Guard == nil {
		cfg.Guard = NewGuard()
	}
	cfg.Guard.AllowHost(cfg.BaseURL)
	cfg.Limits = cfg.Limits.WithDefaults()
	s := &Server{
		cfg: cfg, mux: http.NewServeMux(),
		requests: newLimiter(cfg.Limits.MaxConcurrentRequests),
		uploads:  newLimiter(cfg.Limits.MaxConcurrentUpload),
	}
	cfg.Processor.SessionState = s.sessionState
	cfg.Processor.MaxCallsInRequest = cfg.Limits.MaxCallsInRequest

	s.mux.HandleFunc("GET /.well-known/jmap", s.handleSession)
	s.mux.HandleFunc("GET /jmap/session", s.handleSession)
	s.mux.HandleFunc("POST /jmap/api", s.handleAPI)
	s.mux.HandleFunc("GET /jmap/eventsource", s.handleEventSource)
	s.mux.Handle("GET /jmap/download/{accountId}/{blobId}/{name}", s.blobs(s.handleDownload))
	s.mux.Handle("POST /jmap/upload/{accountId}/", s.blobs(s.handleUpload))
	s.guarded = cfg.Guard.Handler(http.HandlerFunc(s.serve))
	return s
}

// Guard returns the Server's Guard: the one it was given, or the one it
// made.
func (s *Server) Guard() *Guard { return s.cfg.Guard }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.guarded.ServeHTTP(w, r)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFrom(r.Context()); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// user returns the request's authenticated user, which ServeHTTP has already
// insisted on.
func user(ctx context.Context) *auth.User {
	u, _ := auth.UserFrom(ctx)
	return u
}

func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return strings.TrimSuffix(s.cfg.BaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// accounts builds the session's accounts object for a user: every account of
// the user's that has data here.
func (s *Server) accounts(u *auth.User) map[string]any {
	out := map[string]any{}
	for _, id := range u.Accounts {
		acct := s.cfg.Account(id)
		if acct == nil {
			continue
		}
		name := id
		if a := s.cfg.Directory.Accounts[id]; a != nil && a.Name != "" {
			name = a.Name
		}
		out[id] = map[string]any{
			"name":                name,
			"isPersonal":          true,
			"isReadOnly":          false,
			"accountCapabilities": acct.Capabilities(),
		}
	}
	return out
}

// primaryAccounts names the user's primary account for each capability that
// account has.
func (s *Server) primaryAccounts(u *auth.User) map[string]string {
	out := map[string]string{}
	if u.PrimaryAccount == "" {
		return out
	}
	acct := s.cfg.Account(u.PrimaryAccount)
	if acct == nil {
		return out
	}
	for capability := range acct.Capabilities() {
		out[capability] = u.PrimaryAccount
	}
	return out
}

// session builds the Session object (RFC 8620 §2) for the request's user.
func (s *Server) session(r *http.Request) map[string]any {
	base := s.baseURL(r)
	u := user(r.Context())
	l := s.cfg.Limits
	capabilities := map[string]any{
		jmap.CoreCapability: map[string]any{
			"maxSizeUpload":         l.MaxSizeUpload,
			"maxConcurrentUpload":   l.MaxConcurrentUpload,
			"maxSizeRequest":        l.MaxSizeRequest,
			"maxConcurrentRequests": l.MaxConcurrentRequests,
			"maxCallsInRequest":     l.MaxCallsInRequest,
			"maxObjectsInGet":       l.MaxObjectsInGet,
			"maxObjectsInSet":       l.MaxObjectsInSet,
			"collationAlgorithms":   s.cfg.Collations,
		},
	}
	for _, capability := range s.cfg.Processor.Capabilities() {
		if capability != jmap.CoreCapability {
			capabilities[capability] = map[string]any{}
		}
	}
	for capability, details := range s.cfg.Capabilities {
		capabilities[capability] = details
	}
	return map[string]any{
		"capabilities":    capabilities,
		"accounts":        s.accounts(u),
		"primaryAccounts": s.primaryAccounts(u),
		"username":        u.ID,
		"apiUrl":          base + "/jmap/api",
		"downloadUrl":     base + "/jmap/download/{accountId}/{blobId}/{name}?type={type}",
		"uploadUrl":       base + "/jmap/upload/{accountId}/",
		"eventSourceUrl":  base + "/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
		"state":           s.sessionState(r.Context()),
	}
}

// sessionState only has to change when the session changes.  Apart from the
// URLs, which vary by request and not by session, what varies is the user's
// accounts and their capabilities, so a digest of those is an honest state
// string.
func (s *Server) sessionState(ctx context.Context) string {
	u := user(ctx)
	if u == nil {
		return ""
	}
	digest, _ := json.Marshal([]any{u.ID, u.PrimaryAccount, s.accounts(u)})
	sum := sha256.Sum256(digest)
	return hex.EncodeToString(sum[:8])
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	writeJSON(w, http.StatusOK, s.session(r))
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	release, ok := s.requests.acquire(r.Context(), user(r.Context()).ID)
	if !ok {
		return
	}
	defer release()

	limit := s.cfg.Limits.MaxSizeRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		requestError(w, &jmap.RequestError{Type: jmap.ReqErrNotRequest, Status: 400, Detail: "could not read the request body"})
		return
	}
	if int64(len(body)) > limit {
		requestError(w, jmap.LimitError("maxSizeRequest", "the request is too large"))
		return
	}
	req, rerr := jmap.ParseRequest(body)
	if rerr != nil {
		requestError(w, rerr)
		return
	}
	resp, rerr := s.cfg.Processor.Process(r.Context(), req)
	if rerr != nil {
		requestError(w, rerr)
		return
	}
	for _, inv := range resp.MethodResponses {
		if inv.Name == "error" {
			s.cfg.Logger.Info("method error", "callId", inv.CallID, "error", string(inv.Args))
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// blobs guards a blob endpoint: the account named in the path must be one
// the caller may use, and one it may not use is no more found than one that
// doesn't exist.
func (s *Server) blobs(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("accountId")
		if s.cfg.Blobs == nil || !user(r.Context()).HasAccount(id) || s.cfg.Account(id) == nil {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	})
}

// currentStates reads the state of every type in every one of the user's
// accounts.
func (s *Server) currentStates(ctx context.Context, u *auth.User) (push.Changes, error) {
	out := push.Changes{}
	ids := append([]string(nil), u.Accounts...)
	sort.Strings(ids)
	for _, id := range ids {
		acct := s.cfg.Account(id)
		if acct == nil {
			continue
		}
		states, err := acct.States(ctx)
		if err != nil {
			return nil, err
		}
		out[id] = states
	}
	return out, nil
}

func requestError(w http.ResponseWriter, e *jmap.RequestError) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(e.Status)
	json.NewEncoder(w).Encode(e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}
