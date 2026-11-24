// Package auth knows who is asking.  It holds the directory of users,
// accounts and credentials, and provides the HTTP middleware that turns a
// connection into an authenticated User in the request context.
//
// The model is small on purpose.  A user is an actor who can authenticate.
// An account is a JMAP account, a separate body of data, owned by exactly
// one user who has complete access to it; a user may own several.  A
// credential is one way a user proves who they are.  There is no sharing,
// no admin, and no notion of rights beyond ownership.  Where an account's
// data lives is the application's business, not this package's.
//
// A credential may name some of its user's accounts, in which case a
// request authenticated by it sees only those, as if the user owned
// nothing else.  That allows, say, a device that should see only a work
// account without inventing a second user to own it.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
)

// Credential types.
const (
	// CredLoopback means "a request arriving on the plain, unauthenticated
	// listener".  At most one user may hold it.
	CredLoopback = "loopback"

	// CredTailscale identifies a user by the login name Tailscale reports
	// for the node the request came from.
	CredTailscale = "tailscale"

	// CredBearer identifies a user by the bearer token in the request's
	// Authorization header (RFC 6750).  The directory holds only the
	// token's SHA-256 digest.
	CredBearer = "bearer"
)

type Credential struct {
	Type string

	// Login is the Tailscale login name (like "rjbs@github") for a
	// CredTailscale credential, and empty otherwise.
	Login string

	// TokenHash is the lowercase hex SHA-256 digest of the token for a
	// CredBearer credential, and empty otherwise.
	TokenHash string

	// AccountIDs, if not nil, limits requests made with this credential
	// to those accounts, which the user must own.  An empty, non-nil
	// list is an error, since such a credential could see nothing.
	AccountIDs []string
}

type User struct {
	ID          string
	Credentials []Credential

	// Accounts lists the ids of the accounts this user owns, sorted.
	Accounts []string

	// PrimaryAccount is the account the Session object advertises as
	// primary for every capability, or empty for none.  RFC 8620 §2 lets
	// a Session name no primary account.
	PrimaryAccount string
}

// limitedTo returns a copy of the user that owns only the given accounts.
// The copy keeps the primary account only if it is among them.
func (u *User) limitedTo(ids []string) *User {
	v := *u
	v.Accounts = slices.Compact(slices.Sorted(slices.Values(ids)))
	if !v.HasAccount(v.PrimaryAccount) {
		v.PrimaryAccount = ""
	}
	return &v
}

// HasAccount reports whether the user owns the account.
func (u *User) HasAccount(id string) bool {
	_, found := slices.BinarySearch(u.Accounts, id)
	return found
}

type Account struct {
	ID    string
	Name  string
	Owner string
}

// Directory is the complete set of users and accounts.  It is built once
// at startup and never changes while the server runs.
type Directory struct {
	Users    map[string]*User
	Accounts map[string]*Account

	// TokenPrefix begins every bearer token the directory will accept.
	// See NewToken.
	TokenPrefix string

	loopback  *User
	tailscale map[string]*User
	bearer    map[string]*User // by token digest
}

// NewDirectory links users to the accounts they own and indexes
// credentials.  It reports a bad directory rather than guessing: an
// account with an unknown owner, the loopback credential or a tailscale
// login held twice, or a primary account or credential account the user
// does not own.  A user who owns exactly one account and names no primary
// gets that one as primary; a user who owns several gets none unless one
// is named.
func NewDirectory(users map[string]*User, accounts map[string]*Account) (*Directory, error) {
	d := &Directory{
		Users:     users,
		Accounts:  accounts,
		tailscale: map[string]*User{},
		bearer:    map[string]*User{},
	}
	for _, u := range users {
		u.Accounts = nil
	}
	for id, a := range accounts {
		a.ID = id
		owner, ok := users[a.Owner]
		if !ok {
			return nil, fmt.Errorf("account %q is owned by unknown user %q", id, a.Owner)
		}
		owner.Accounts = append(owner.Accounts, id)
	}
	for id, u := range users {
		u.ID = id
		sort.Strings(u.Accounts)
		switch {
		case len(u.Accounts) == 0:
			return nil, fmt.Errorf("user %q owns no accounts", id)
		case u.PrimaryAccount == "" && len(u.Accounts) == 1:
			u.PrimaryAccount = u.Accounts[0]
		case u.PrimaryAccount != "" && !u.HasAccount(u.PrimaryAccount):
			return nil, fmt.Errorf("user %q names primaryAccount %q, which it does not own", id, u.PrimaryAccount)
		}
		for _, c := range u.Credentials {
			as := u
			if c.AccountIDs != nil {
				if len(c.AccountIDs) == 0 {
					return nil, fmt.Errorf("user %q has a credential with an empty accountIds list", id)
				}
				for _, a := range c.AccountIDs {
					if !u.HasAccount(a) {
						return nil, fmt.Errorf("user %q has a credential for account %q, which it does not own", id, a)
					}
				}
				as = u.limitedTo(c.AccountIDs)
			}
			switch c.Type {
			case CredLoopback:
				if d.loopback != nil {
					return nil, claimedTwice(d.loopback.ID, id, "the loopback credential")
				}
				d.loopback = as
			case CredTailscale:
				if c.Login == "" {
					return nil, fmt.Errorf("user %q has a tailscale credential with no login", id)
				}
				if other, dup := d.tailscale[c.Login]; dup {
					return nil, claimedTwice(other.ID, id, fmt.Sprintf("tailscale login %q", c.Login))
				}
				d.tailscale[c.Login] = as
			case CredBearer:
				digest := strings.ToLower(c.TokenHash)
				if b, err := hex.DecodeString(digest); err != nil || len(b) != sha256.Size {
					return nil, fmt.Errorf("user %q has a bearer credential whose tokenHash is not a hex SHA-256 digest", id)
				}
				if other, dup := d.bearer[digest]; dup {
					return nil, claimedTwice(other.ID, id, "a bearer token")
				}
				d.bearer[digest] = as
			default:
				return nil, fmt.Errorf("user %q has a credential of unknown type %q", id, c.Type)
			}
		}
	}
	return d, nil
}

// claimedTwice reports a credential held twice.  Even one user may not
// hold it twice, because each holding could limit it to a different
// account.
func claimedTwice(first, second, what string) error {
	if first == second {
		return fmt.Errorf("user %q claims %s twice", first, what)
	}
	return fmt.Errorf("users %q and %q both claim %s", first, second, what)
}

// Solo returns the directory of a server with one user, who owns its one
// account, and is who every request acts as.  It is to be used with
// Loopback: who may make requests at all is for whoever lets them in to
// decide, by where the server listens and what stands in front of it.
func Solo(username, accountID string) *Directory {
	d, err := NewDirectory(
		map[string]*User{username: {Credentials: []Credential{{Type: CredLoopback}}}},
		map[string]*Account{accountID: {Owner: username, Name: username}},
	)
	if err != nil {
		// One user owning one account can't be a bad directory.
		panic(err)
	}
	return d
}

// LoopbackUser is the user that plain-listener requests act as, or nil if
// no user holds the loopback credential.  If the credential names
// accounts, the user returned owns only those.
func (d *Directory) LoopbackUser() *User { return d.loopback }

// BearerUser is the user holding the given bearer token, or nil.  If the
// credential names accounts, the user returned owns only those.
//
// Only digests are stored, so the lookup compares digests of the token
// rather than the token itself, and a map lookup's timing reveals nothing
// useful about any stored token.  -- claude, 2026-11-07
func (d *Directory) BearerUser(token string) *User {
	return d.bearer[HashToken(token)]
}

// TailscaleUser is the user holding the given Tailscale login, or nil.
// If the credential names accounts, the user returned owns only those.
func (d *Directory) TailscaleUser(login string) *User { return d.tailscale[login] }

type ctxKey struct{}

// WithUser returns a context carrying the authenticated user.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// UserFrom returns the authenticated user, if the request has one.
func UserFrom(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(ctxKey{}).(*User)
	return u, ok && u != nil
}

// Bearer is middleware for any listener.  A request with a bearer token
// in its Authorization header acts as the user holding that token, and
// one with an unknown token is refused, rather than passed along to be
// identified by its listener: a client that sent a token expects to be
// that token's user, and quietly being someone else would hide a broken
// token.  Requests without a bearer token go to next unchanged, which
// identifies them by the listener's own means.  Refusals are reported
// through reject.
func Bearer(d *Directory, reject func(r *http.Request, why string), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			next.ServeHTTP(w, r)
			return
		}
		token = strings.TrimSpace(token)
		if !WellFormedToken(d.TokenPrefix, token) {
			reject(r, "malformed bearer token")
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "malformed bearer token", http.StatusUnauthorized)
			return
		}
		u := d.BearerUser(token)
		if u == nil {
			reject(r, "unknown bearer token")
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "unknown bearer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

// Loopback is middleware for the plain listener: every request acts as the
// loopback user, unless it has already been authenticated, as by Bearer.
// With no loopback user configured, every other request is refused.
func Loopback(d *Directory, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFrom(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		u := d.LoopbackUser()
		if u == nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "no user is configured for unauthenticated access", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

// Identity is what a Tailscale lookup says about the far end of a
// connection.
type Identity struct {
	Login  string
	Tagged bool
}

// WhoIsFunc resolves a connection's remote address to its Tailscale
// identity.
type WhoIsFunc func(ctx context.Context, remoteAddr string) (Identity, error)

// Tailscale is middleware for the tailnet listener: the request acts as the
// user holding the connecting node's login as a credential, unless it has
// already been authenticated, as by Bearer.  Otherwise, tagged nodes
// have no user and are refused, as is any login nobody holds.  Refusals are
// reported through reject, so an operator can see which login to add.
func Tailscale(d *Directory, whois WhoIsFunc, reject func(r *http.Request, why string), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFrom(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		id, err := whois(r.Context(), r.RemoteAddr)
		if err != nil {
			reject(r, "whois failed: "+err.Error())
			http.Error(w, "could not identify the connecting node", http.StatusUnauthorized)
			return
		}
		if id.Tagged {
			reject(r, "tagged node")
			http.Error(w, "tagged nodes may not use this service", http.StatusForbidden)
			return
		}
		u := d.TailscaleUser(id.Login)
		if u == nil {
			reject(r, fmt.Sprintf("no user holds tailscale login %q", id.Login))
			http.Error(w, "no user is configured for this tailscale login", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}
