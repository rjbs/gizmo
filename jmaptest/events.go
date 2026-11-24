package jmaptest

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Event is one event from an event source.
type Event struct {
	Type string
	ID   string
	Data string
}

// Stream is an open event source connection.
type Stream struct {
	t      testing.TB
	events chan Event
	cancel context.CancelFunc

	// Status is the HTTP status the connection was answered with.  Unless
	// it is 200, there will be no events.
	Status int

	// Header is the headers the connection was answered with.
	Header http.Header
}

// Listen opens the event source with the given query string, such as
// "types=*&closeafter=no&ping=0".  The connection is closed when the test
// ends, or by Close.
func (c *Tester) Listen(query string) *Stream {
	c.T.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/jmap/eventsource?"+query, nil)
	if err != nil {
		cancel()
		c.T.Fatal(err)
	}
	if c.Authorization != "" {
		req.Header.Set("Authorization", c.Authorization)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		cancel()
		c.T.Fatalf("opening the event source: %v", err)
	}
	s := &Stream{t: c.T, events: make(chan Event, 64), cancel: cancel, Status: resp.StatusCode, Header: resp.Header}
	c.T.Cleanup(s.Close)
	go func() {
		defer resp.Body.Close()
		defer close(s.events)
		if resp.StatusCode != 200 {
			return
		}
		var ev Event
		var data []string
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.Type != "" || len(data) > 0 {
					ev.Data = strings.Join(data, "\n")
					s.events <- ev
				}
				ev, data = Event{}, nil
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "event:"):
				ev.Type = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "id:"):
				ev.ID = strings.TrimSpace(line[len("id:"):])
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimPrefix(line[len("data:"):], " "))
			}
		}
	}()
	return s
}

// Close ends the connection.
func (s *Stream) Close() { s.cancel() }

// next waits for the next event.  The boolean is false if none came in time
// or the server closed the connection.
func (s *Stream) next(wait time.Duration) (Event, bool, bool) {
	select {
	case ev, open := <-s.events:
		return ev, open, !open
	case <-time.After(wait):
		return Event{}, false, false
	}
}

// Decode decodes the event's data, which must be JSON, into a value of the
// test's own type.
func (e Event) Decode(t testing.TB, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(e.Data), into); err != nil {
		t.Fatalf("decoding the data of a %q event: %v: %s", e.Type, err, e.Data)
	}
}

// Next returns the next event, which must come within a couple of seconds.
func (s *Stream) Next() Event {
	s.t.Helper()
	ev, ok, closed := s.next(2 * time.Second)
	if closed {
		s.t.Fatal("expected an event, but the connection was closed")
	}
	if !ok {
		s.t.Fatal("expected an event, but none came")
	}
	return ev
}

// NextState returns the "changed" member of the next event, which must be a
// StateChange and must come within a couple of seconds: account id to type
// name to state.
func (s *Stream) NextState() map[string]map[string]string {
	s.t.Helper()
	ev := s.Next()
	var change struct {
		Type    string                       `json:"@type"`
		Changed map[string]map[string]string `json:"changed"`
	}
	if ev.Type != "state" || json.Unmarshal([]byte(ev.Data), &change) != nil || change.Type != "StateChange" {
		s.t.Fatalf("expected a state change, but got %q: %s", ev.Type, ev.Data)
	}
	return change.Changed
}

// AssertQuiet fails the test if an event comes within a moment.
func (s *Stream) AssertQuiet() {
	s.t.Helper()
	if ev, ok, _ := s.next(150 * time.Millisecond); ok {
		s.t.Fatalf("expected no event, but got %q: %s", ev.Type, ev.Data)
	}
}

// AssertClosed fails the test unless the server closes the connection within
// a couple of seconds, sending nothing more first.
func (s *Stream) AssertClosed() {
	s.t.Helper()
	ev, ok, closed := s.next(2 * time.Second)
	if ok {
		s.t.Fatalf("expected the connection to be closed, but got %q: %s", ev.Type, ev.Data)
	}
	if !closed {
		s.t.Fatal("expected the connection to be closed, but it stayed open")
	}
}
