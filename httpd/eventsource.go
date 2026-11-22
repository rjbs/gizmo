package httpd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rjbs/gizmo/push"
)

// handleEventSource is the push event source (RFC 8620 §7.3).
func (s *Server) handleEventSource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wanted := map[string]bool{}
	for _, t := range strings.Split(q.Get("types"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			wanted[t] = true
		}
	}
	all := len(wanted) == 0 || wanted["*"]

	closeAfter := q.Get("closeafter")
	if closeAfter != "" && closeAfter != "state" && closeAfter != "no" {
		http.Error(w, "closeafter must be state or no", http.StatusBadRequest)
		return
	}
	closeAfterState := closeAfter == "state"

	pingSeconds := 0
	if p := q.Get("ping"); p != "" {
		var err error
		pingSeconds, err = strconv.Atoi(p)
		if err != nil || pingSeconds < 0 {
			http.Error(w, "ping must be a whole number of seconds", http.StatusBadRequest)
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	u := user(r.Context())

	// Subscribing comes before reading the states, so that a change
	// committed between the two is heard of one way or the other.
	sub := s.cfg.Broker.Subscribe(u.Accounts)
	defer sub.Close()

	var initial push.Changes
	if !closeAfterState {
		var err error
		if initial, err = s.currentStates(r.Context(), u); err != nil {
			s.cfg.Logger.Error("reading states for event source", "error", err)
			http.Error(w, "could not read the current states", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	eventID := 0
	sendPing := func() {
		fmt.Fprintf(w, "event: ping\ndata: {\"interval\":%d}\n\n", pingSeconds)
		flusher.Flush()
	}
	sendState := func(changed push.Changes) bool {
		for acct, types := range changed {
			if !all {
				for typ := range types {
					if !wanted[typ] {
						delete(types, typ)
					}
				}
			}
			if len(types) == 0 {
				delete(changed, acct)
			}
		}
		if len(changed) == 0 {
			return false
		}
		eventID++
		data, _ := json.Marshal(map[string]any{
			"@type":   "StateChange",
			"changed": changed,
		})
		fmt.Fprintf(w, "event: state\nid: %d\ndata: %s\n\n", eventID, data)
		flusher.Flush()
		return true
	}

	// Send something at once: some browsers (Firefox) do not fire the
	// EventSource "open" event until the first byte of the body arrives.
	// The current state is the most useful first byte, since a client
	// that fell behind while disconnected learns so immediately.  A
	// closeafter=state client wants to be held until a real change, so
	// it gets a comment instead.  -- claude, 2026-10-02
	if closeAfterState || !sendState(initial) {
		fmt.Fprint(w, ": hello\n\n")
		flusher.Flush()
	}

	var ping <-chan time.Time
	if pingSeconds > 0 {
		ticker := time.NewTicker(time.Duration(pingSeconds) * time.Second)
		defer ticker.Stop()
		ping = ticker.C
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping:
			sendPing()
		case <-sub.Notify():
			if sendState(sub.Take()) && closeAfterState {
				return
			}
		}
	}
}
