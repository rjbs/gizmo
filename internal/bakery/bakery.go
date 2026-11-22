// Package bakery is a small JMAP server kept for Gizmo's own tests, in the
// way that Ix kept Bakesale.  It serves one type, Cookie, from memory, with
// methods written by hand, and stores blobs the same way.
//
// It has two users.  alice owns the accounts "kitchen" and "cellar", with
// "kitchen" her primary; requests with no token are hers.  bob owns "bobs".
// Each has a bearer token, and alice has a second that sees only "cellar".
// There is also an account with no data, "attic", which is alice's.
package bakery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"

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
}

// Account is one account's data.
type Account struct {
	id     string
	broker *push.Broker

	mu      sync.Mutex
	cookies map[string]string // id to flavor
	nextID  int
	state   int
	blobs   map[string]blob
}

type blob struct {
	data        []byte
	contentType string
}

// New returns a Bakery with nothing baked.  The limits are those the server
// advertises and enforces; zero fields take the defaults.
func New(limits jmap.Limits) *Bakery {
	b := &Bakery{Broker: push.NewBroker(), Processor: jmap.NewProcessor(), accounts: map[string]*Account{}}
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

	for _, id := range []string{"kitchen", "cellar", "bobs"} {
		b.accounts[id] = &Account{id: id, broker: b.Broker, cookies: map[string]string{}, blobs: map[string]blob{}}
	}

	b.Processor.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	find := func(id string) (*Account, bool) { a, ok := b.accounts[id]; return a, ok }
	b.Processor.Register("Cookie/get", Capability, auth.ForAccount(find, (*Account).get))
	b.Processor.Register("Cookie/set", Capability, auth.ForAccount(find, (*Account).set))

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
		Broker:       b.Broker,
		Limits:       limits,
		Capabilities: map[string]any{Capability: map[string]any{}},
		Download:     http.HandlerFunc(b.download),
		Upload:       http.HandlerFunc(b.upload),
		Logger:       b.Processor.Logger,
	})
	b.Handler = auth.Bearer(dir, func(*http.Request, string) {}, auth.Loopback(dir, srv))
	return b
}

// Account returns an account's data.
func (b *Bakery) Account(id string) *Account { return b.accounts[id] }

func (a *Account) Capabilities() map[string]any {
	return map[string]any{Capability: map[string]any{"oven": a.id}}
}

func (a *Account) States(context.Context) (map[string]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]string{"Cookie": strconv.Itoa(a.state)}, nil
}

// bake adds a cookie.  The caller holds the lock.  Ids are all digits, with
// leading zeros, the better to find anything that takes an id for a number.
func (a *Account) bake(flavor string) string {
	a.nextID++
	a.state++
	id := fmt.Sprintf("%03d", a.nextID)
	a.cookies[id] = flavor
	return id
}

func (a *Account) publish() {
	a.broker.Publish(a.id, map[string]string{"Cookie": strconv.Itoa(a.state)})
}

// Bake adds a cookie as a writer inside the server but outside any request
// would, publishing the change.
func (a *Account) Bake(flavor string) string {
	a.mu.Lock()
	id := a.bake(flavor)
	a.publish()
	a.mu.Unlock()
	return id
}

// Sneak adds a cookie as another process would: nothing is published.
func (a *Account) Sneak(flavor string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bake(flavor)
}

func (a *Account) get(c *jmap.Call, raw json.RawMessage) (any, error) {
	var args struct {
		AccountID string    `json:"accountId"`
		IDs       *[]string `json:"ids"`
	}
	if err := jmap.DecodeArgs(raw, &args); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	var ids []string
	if args.IDs == nil {
		for id := range a.cookies {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	} else {
		for _, id := range *args.IDs {
			real, ok := c.ResolveID(id)
			if !ok {
				return nil, jmap.ArgError("nothing was created as %s", id)
			}
			ids = append(ids, real)
		}
	}
	list, notFound := []any{}, []string{}
	for _, id := range ids {
		if flavor, ok := a.cookies[id]; ok {
			list = append(list, map[string]any{"id": id, "flavor": flavor})
		} else {
			notFound = append(notFound, id)
		}
	}
	return map[string]any{
		"accountId": a.id,
		"state":     strconv.Itoa(a.state),
		"list":      list,
		"notFound":  notFound,
	}, nil
}

func (a *Account) set(c *jmap.Call, raw json.RawMessage) (any, error) {
	var args struct {
		AccountID string `json:"accountId"`
		Create    map[string]struct {
			Flavor string `json:"flavor"`
		} `json:"create"`
		Destroy []string `json:"destroy"`
	}
	if err := jmap.DecodeArgs(raw, &args); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	oldState := a.state

	destroyed, notDestroyed := []string{}, map[string]any{}
	for _, id := range args.Destroy {
		if _, ok := a.cookies[id]; !ok {
			notDestroyed[id] = jmap.NotFound()
			continue
		}
		delete(a.cookies, id)
		a.state++
		destroyed = append(destroyed, id)
	}

	created, notCreated := map[string]any{}, map[string]any{}
	creationIDs := make([]string, 0, len(args.Create))
	for cid := range args.Create {
		creationIDs = append(creationIDs, cid)
	}
	sort.Strings(creationIDs)
	for _, cid := range creationIDs {
		flavor := args.Create[cid].Flavor
		if flavor == "" {
			notCreated[cid] = jmap.InvalidProperties("a cookie needs a flavor", "flavor")
			continue
		}
		id := a.bake(flavor)
		c.SetCreatedID(cid, id)
		created[cid] = map[string]any{"id": id}
	}

	if a.state != oldState {
		a.publish()
	}
	return map[string]any{
		"accountId":    a.id,
		"oldState":     strconv.Itoa(oldState),
		"newState":     strconv.Itoa(a.state),
		"created":      created,
		"notCreated":   notCreated,
		"destroyed":    destroyed,
		"notDestroyed": notDestroyed,
	}, nil
}

func (b *Bakery) upload(w http.ResponseWriter, r *http.Request) {
	a := b.accounts[r.PathValue("accountId")]
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "could not read the upload", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(data)
	id := "B" + hex.EncodeToString(sum[:8])
	contentType := r.Header.Get("Content-Type")
	a.mu.Lock()
	a.blobs[id] = blob{data: data, contentType: contentType}
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{"accountId": a.id, "blobId": id, "type": contentType, "size": len(data)})
}

func (b *Bakery) download(w http.ResponseWriter, r *http.Request) {
	a := b.accounts[r.PathValue("accountId")]
	a.mu.Lock()
	bl, ok := a.blobs[r.PathValue("blobId")]
	a.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	contentType := bl.contentType
	if t := r.URL.Query().Get("type"); t != "" {
		contentType = t
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(bl.data)
}
