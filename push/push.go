// Package push carries state changes from whatever makes them to whoever is
// listening, such as the event source connections of RFC 8620 §7.3.
//
// Anything may publish: the commit of a /set, a background worker, or
// [Watch], which looks for changes made by other processes.
package push

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Changes maps account id to type name to state string, the shape of the
// "changed" member of a StateChange object.
type Changes map[string]map[string]string

// Broker fans state changes out to subscribers.  Each subscription keeps
// only the newest state of each type, so a slow reader gets one change
// where a fast one got several, and never blocks the publisher.
type Broker struct {
	mu   sync.Mutex
	subs map[*Subscription]struct{}
	last Changes
}

func NewBroker() *Broker {
	return &Broker{subs: map[*Subscription]struct{}{}, last: Changes{}}
}

// Publish tells every subscription watching the account of the new states of
// some of its types.  A state that is the same as the last one published or
// primed for its type is not news, and is dropped, so it does no harm to
// publish the same change twice, or to publish every state when only one may
// have changed.
func (b *Broker) Publish(accountID string, states map[string]string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	changed := b.note(accountID, states)
	if len(changed) == 0 {
		return
	}
	for s := range b.subs {
		if !s.accounts[accountID] {
			continue
		}
		s.mu.Lock()
		if s.pending[accountID] == nil {
			s.pending[accountID] = map[string]string{}
		}
		for typ, state := range changed {
			s.pending[accountID][typ] = state
		}
		s.mu.Unlock()
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
}

// Prime records the states of an account's types without telling anyone, so
// that publishing them again later is not taken for a change.
func (b *Broker) Prime(accountID string, states map[string]string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.note(accountID, states)
}

// note records states as the last published and returns those that differed.
func (b *Broker) note(accountID string, states map[string]string) map[string]string {
	if b.last[accountID] == nil {
		b.last[accountID] = map[string]string{}
	}
	changed := map[string]string{}
	for typ, state := range states {
		if prev, ok := b.last[accountID][typ]; ok && prev == state {
			continue
		}
		b.last[accountID][typ] = state
		changed[typ] = state
	}
	return changed
}

// Publisher returns a function that publishes changes to one account, for
// handing to whatever commits them.
func (b *Broker) Publisher(accountID string) func(states map[string]string) {
	return func(states map[string]string) { b.Publish(accountID, states) }
}

// Subscription is one listener's view of the changes to some accounts.
type Subscription struct {
	b        *Broker
	mu       sync.Mutex
	accounts map[string]bool
	pending  Changes
	notify   chan struct{}
}

// Subscribe begins a subscription to changes in the given accounts.  It must
// be closed when done with.
func (b *Broker) Subscribe(accounts []string) *Subscription {
	s := &Subscription{b: b, accounts: map[string]bool{}, pending: Changes{}, notify: make(chan struct{}, 1)}
	for _, a := range accounts {
		s.accounts[a] = true
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Notify returns a channel that receives when there are changes to Take.
func (s *Subscription) Notify() <-chan struct{} { return s.notify }

// Take returns the changes published since the last Take, and forgets them.
func (s *Subscription) Take() Changes {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending
	s.pending = Changes{}
	return out
}

// Close ends the subscription.
func (s *Subscription) Close() {
	s.b.mu.Lock()
	delete(s.b.subs, s)
	s.b.mu.Unlock()
}

// Watch publishes the changes to an account that nothing else will: those
// made by another process writing to the account's database.  Every interval
// it asks for the account's states and publishes them, and the broker passes
// on the ones that have changed.  It returns when ctx is done.
//
// A server whose every writer is in its own process, and publishes for
// itself, has no need of this.
func Watch(ctx context.Context, b *Broker, accountID string, interval time.Duration, states func(context.Context) (map[string]string, error)) {
	if now, err := states(ctx); err == nil {
		b.Prime(accountID, now)
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now, err := states(ctx)
			if err != nil {
				if ctx.Err() == nil {
					slog.Default().ErrorContext(ctx, "reading states to watch for changes", "account", accountID, "error", err)
				}
				continue
			}
			b.Publish(accountID, now)
		}
	}
}
