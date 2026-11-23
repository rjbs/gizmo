package httpd

import (
	"context"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := newLimiter(2)
	ctx := context.Background()

	// held reports whether a place can't be had within a moment.
	held := func(user string) bool {
		t.Helper()
		short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		release, ok := l.acquire(short, user)
		if ok {
			release()
		}
		return !ok
	}

	first, ok1 := l.acquire(ctx, "alice")
	second, ok2 := l.acquire(ctx, "alice")
	if !ok1 || !ok2 {
		t.Fatal("two places should be had at once")
	}
	if !held("alice") {
		t.Error("a third place was had while two were taken")
	}
	if held("bob") {
		t.Error("one user's places kept another from having one")
	}

	// Someone waiting gets the place as soon as it is given back.
	got := make(chan bool, 1)
	go func() {
		release, ok := l.acquire(ctx, "alice")
		if ok {
			release()
		}
		got <- ok
	}()
	time.Sleep(20 * time.Millisecond)
	first()
	select {
	case ok := <-got:
		if !ok {
			t.Error("the one waiting was turned away")
		}
	case <-time.After(2 * time.Second):
		t.Error("the one waiting never got the place given back")
	}
	second()
	if held("alice") {
		t.Error("with every place given back, none could be had")
	}
}
