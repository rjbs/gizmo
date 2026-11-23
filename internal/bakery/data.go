package bakery

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"

	"github.com/rjbs/gizmo/push"
	"github.com/rjbs/gizmo/std"
)

// Account is one account's data.
type Account struct {
	id     string
	broker *push.Broker
	limits std.Limits

	mu sync.Mutex
	d  *data
}

// data is everything in an account.  A transaction works on a copy of it,
// and a savepoint on a copy of that, which is extravagant and simple.
type data struct {
	cookies map[string]*cookie
	jars    map[string]*jar
	graves  []grave
	modseq  map[string]int // by type
	nextID  int
	blobs   map[string]blob
}

type cookie struct {
	flavor string
	notes  map[string]any

	// bakedBy is the user who made the cookie, and batch the id of the
	// request it was made in.  Both are empty for a cookie not made by a
	// request.
	bakedBy, batch string

	created, modified int
}

type jar struct {
	name      string
	cookieIDs []string

	created, modified int
}

// grave is what is left of a destroyed object.
type grave struct {
	typ, id           string
	created, modified int
}

type blob struct {
	data        []byte
	contentType string
}

func newData() *data {
	return &data{cookies: map[string]*cookie{}, jars: map[string]*jar{}, modseq: map[string]int{}, blobs: map[string]blob{}}
}

func (d *data) clone() *data {
	out := &data{
		cookies: make(map[string]*cookie, len(d.cookies)),
		jars:    make(map[string]*jar, len(d.jars)),
		graves:  slices.Clone(d.graves),
		modseq:  maps.Clone(d.modseq),
		nextID:  d.nextID,
		blobs:   maps.Clone(d.blobs),
	}
	for id, c := range d.cookies {
		cc := *c
		cc.notes = maps.Clone(c.notes)
		out.cookies[id] = &cc
	}
	for id, j := range d.jars {
		jj := *j
		jj.cookieIDs = slices.Clone(j.cookieIDs)
		out.jars[id] = &jj
	}
	return out
}

// bump gives a type its next modseq.
func (d *data) bump(typ string) int {
	d.modseq[typ]++
	return d.modseq[typ]
}

// newID makes an id.  Ids are all digits, with leading zeros, the better to
// find anything that takes an id for a number.
func (d *data) newID() string {
	d.nextID++
	return fmt.Sprintf("%03d", d.nextID)
}

func (d *data) states() map[string]string {
	return map[string]string{
		"Cookie": strconv.Itoa(d.modseq["Cookie"]),
		"Jar":    strconv.Itoa(d.modseq["Jar"]),
	}
}

// bake adds a cookie.
func (d *data) bake(flavor string) string {
	id := d.newID()
	seq := d.bump("Cookie")
	d.cookies[id] = &cookie{flavor: flavor, created: seq, modified: seq}
	return id
}

// tx is a transaction: a copy of the account's data, which replaces the
// account's when the transaction commits.  The account is locked for as long
// as the transaction lasts.
type tx struct {
	a     *Account
	d     *data
	write bool
	done  bool
}

func (a *Account) begin(_ context.Context, write bool) (*tx, error) {
	a.mu.Lock()
	return &tx{a: a, d: a.d.clone(), write: write}, nil
}

func (t *tx) State(_ context.Context, typ string) (string, error) {
	return strconv.Itoa(t.d.modseq[typ]), nil
}

func (t *tx) Savepoint(_ context.Context, fn func() error) error {
	saved := t.d.clone()
	err := fn()
	if err != nil {
		*t.d = *saved
	}
	return err
}

func (t *tx) Commit() error {
	if t.done {
		return fmt.Errorf("the transaction is over")
	}
	if !t.write {
		return fmt.Errorf("a read transaction can't be committed")
	}
	before := t.a.d.states()
	t.a.d = t.d
	t.done = true
	t.a.mu.Unlock()
	if after := t.d.states(); !maps.Equal(before, after) {
		t.a.broker.Publish(t.a.id, after)
	}
	return nil
}

func (t *tx) Rollback() error {
	if !t.done {
		t.done = true
		t.a.mu.Unlock()
	}
	return nil
}

// Capabilities is the account's accountCapabilities object.
func (a *Account) Capabilities() map[string]any {
	return map[string]any{Capability: map[string]any{"oven": a.id}}
}

// States gives the state of each of the account's types.
func (a *Account) States(context.Context) (map[string]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.d.states(), nil
}

// Bake adds a cookie as a writer inside the server but outside any request
// would, publishing the change.
func (a *Account) Bake(flavor string) string {
	a.mu.Lock()
	id := a.d.bake(flavor)
	states := a.d.states()
	a.mu.Unlock()
	a.broker.Publish(a.id, states)
	return id
}

// Sneak adds a cookie as another process would: nothing is published.
func (a *Account) Sneak(flavor string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.d.bake(flavor)
}
