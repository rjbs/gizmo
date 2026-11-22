package push

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type states = map[string]string

// checkTaken checks what a subscription has pending, given as the JSON of a
// Changes, and that taking it leaves nothing.
func checkTaken(t *testing.T, name string, s *Subscription, want string) {
	t.Helper()
	got, _ := json.Marshal(s.Take())
	if string(got) != want {
		t.Errorf("%s: got %s, want %s", name, got, want)
	}
	if again, _ := json.Marshal(s.Take()); string(again) != `{}` {
		t.Errorf("%s: a second take got %s", name, again)
	}
}

// checkNotified checks whether a subscription has been told there is
// something to take.
func checkNotified(t *testing.T, name string, s *Subscription, want bool) {
	t.Helper()
	got := false
	select {
	case <-s.Notify():
		got = true
	default:
	}
	if got != want {
		t.Errorf("%s: notified is %v, want %v", name, got, want)
	}
}

func TestBroker(t *testing.T) {
	b := NewBroker()
	both := b.Subscribe([]string{"home", "work"})
	work := b.Subscribe([]string{"work"})
	defer both.Close()

	b.Publish("home", states{"Task": "1"})
	checkNotified(t, "a change to a watched account", both, true)
	checkNotified(t, "a change to an account not watched", work, false)
	checkTaken(t, "one change", both, `{"home":{"Task":"1"}}`)
	checkTaken(t, "nothing for the other", work, `{}`)

	b.Publish("home", states{"Task": "2"})
	b.Publish("home", states{"Task": "3", "TaskList": "1"})
	b.Publish("work", states{"Task": "7"})
	checkNotified(t, "several changes, one notice", both, true)
	checkNotified(t, "and no second notice", both, false)
	checkTaken(t, "only the newest state of each type", both, `{"home":{"Task":"3","TaskList":"1"},"work":{"Task":"7"}}`)
	checkNotified(t, "the other is told of its own account's", work, true)
	checkTaken(t, "and sees only that", work, `{"work":{"Task":"7"}}`)

	b.Publish("home", states{"Task": "3", "TaskList": "1"})
	checkNotified(t, "the same states again are not news", both, false)
	b.Publish("home", states{"Task": "3", "TaskList": "2"})
	checkTaken(t, "only what differs is passed on", both, `{"home":{"TaskList":"2"}}`)

	b.Prime("work", states{"Task": "8"})
	checkNotified(t, "priming tells nobody", work, false)
	b.Publish("work", states{"Task": "8"})
	checkNotified(t, "and what was primed is not news", work, false)

	work.Close()
	b.Publish("work", states{"Task": "9"})
	checkTaken(t, "a closed subscription hears no more", work, `{}`)
}

func TestWatch(t *testing.T) {
	b := NewBroker()
	sub := b.Subscribe([]string{"home"})
	defer sub.Close()

	var mu sync.Mutex
	now := states{"Task": "1", "TaskList": "1"}
	read := func(context.Context) (states, error) {
		mu.Lock()
		defer mu.Unlock()
		out := states{}
		for k, v := range now {
			out[k] = v
		}
		return out, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Watch(ctx, b, "home", time.Millisecond, read); close(done) }()

	time.Sleep(20 * time.Millisecond)
	checkNotified(t, "the states found at the start are not changes", sub, false)

	mu.Lock()
	now["Task"] = "2"
	mu.Unlock()
	select {
	case <-sub.Notify():
	case <-time.After(2 * time.Second):
		t.Fatal("a change made behind the broker's back was never published")
	}
	checkTaken(t, "the change found", sub, `{"home":{"Task":"2"}}`)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return when its context was done")
	}
}
