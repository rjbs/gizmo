// Package bakery is a small JMAP server kept for Gizmo's own tests, in the
// way that Ix kept Bakesale.  It serves two types, Cookie and Jar, from
// memory, by way of the std package, and keeps blobs as files in a temporary
// directory, which Close removes.
//
// It has two users.  alice owns the accounts "kitchen" and "cellar", with
// "kitchen" her primary; requests with no token are hers.  bob owns "bobs".
// Each has a bearer token, and alice has a second that sees only "cellar".
// There is also an account with no data, "attic", which is alice's.
package bakery

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/rjbs/gizmo/auth"
	"github.com/rjbs/gizmo/httpd"
	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/push"
)

// Capability is the capability Cookie's methods are under.
const Capability = "https://gizmo.example/bakery"

// TokenPrefix begins the bakery's bearer tokens.
const TokenPrefix = "bakery-"

// Bakery is the server and what a test may want of its insides.
type Bakery struct {
	Directory *auth.Directory
	Broker    *push.Broker
	Processor *jmap.Processor

	// Handler is the whole server: authentication, then JMAP.
	Handler http.Handler

	// AliceToken, CellarToken and BobToken are bearer tokens: alice's,
	// alice's limited to the cellar, and bob's.
	AliceToken, CellarToken, BobToken string

	accounts map[string]*Account
	pantry   *pantry
}

// New returns a Bakery with nothing baked.  The limits are those the server
// advertises and enforces; zero fields take the defaults.
func New(limits jmap.Limits) *Bakery {
	b := &Bakery{Broker: push.NewBroker(), Processor: jmap.NewProcessor(), accounts: map[string]*Account{}, pantry: newPantry()}
	b.AliceToken, _ = auth.NewToken(TokenPrefix)
	b.CellarToken, _ = auth.NewToken(TokenPrefix)
	b.BobToken, _ = auth.NewToken(TokenPrefix)

	dir, err := auth.NewDirectory(
		map[string]*auth.User{
			"alice": {PrimaryAccount: "kitchen", Credentials: []auth.Credential{
				{Type: auth.CredLoopback},
				{Type: auth.CredBearer, TokenHash: auth.HashToken(b.AliceToken)},
				{Type: auth.CredBearer, TokenHash: auth.HashToken(b.CellarToken), AccountIDs: []string{"cellar"}},
			}},
			"bob": {Credentials: []auth.Credential{
				{Type: auth.CredBearer, TokenHash: auth.HashToken(b.BobToken)},
			}},
		},
		map[string]*auth.Account{
			"kitchen": {Owner: "alice", Name: "The Kitchen"},
			"cellar":  {Owner: "alice"},
			"attic":   {Owner: "alice"},
			"bobs":    {Owner: "bob"},
		},
	)
	if err != nil {
		panic(err)
	}
	dir.TokenPrefix = TokenPrefix
	b.Directory = dir

	// The bakery gives few cookies to a query, so that a test of paging
	// needn't bake many.
	limits.MaxQueryLimit = 4
	limits = limits.WithDefaults()
	for _, id := range []string{"kitchen", "cellar", "bobs"} {
		b.accounts[id] = &Account{
			id: id, broker: b.Broker, d: newData(),
			limits: limits,
		}
		// The bakery publishes every type's state when any changes,
		// so the broker has to know where they all began.
		b.Broker.Prime(id, b.accounts[id].d.states())
	}

	b.Processor.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	find := func(id string) (*Account, bool) { a, ok := b.accounts[id]; return a, ok }
	for name, method := range map[string]func(*Account, *jmap.Call, json.RawMessage) (any, error){
		"Cookie/get":     (*Account).cookieGet,
		"Cookie/changes": (*Account).cookieChanges,
		"Cookie/set":     (*Account).cookieSet,
		"Cookie/query":   (*Account).cookieQuery,
		"Cookie/import":  (*Account).cookieImport,
		"Jar/get":        (*Account).jarGet,
		"Jar/set":        (*Account).jarSet,
	} {
		b.Processor.Register(name, Capability, auth.ForAccount(find, method))
	}

	srv := httpd.New(httpd.Config{
		Processor: b.Processor,
		Directory: dir,
		Account: func(id string) httpd.Account {
			// A nil *Account in the interface would not be a nil
			// interface, so the missing case is spelled out.
			if a, ok := b.accounts[id]; ok {
				return a
			}
			return nil
		},
		Broker: b.Broker,
		Limits: limits,
		Blobs:  b.pantry,
		Logger: b.Processor.Logger,
	})
	b.Handler = auth.Bearer(dir, func(*http.Request, string) {}, auth.Loopback(dir, srv))
	return b
}

// Account returns an account's data.
func (b *Bakery) Account(id string) *Account { return b.accounts[id] }
