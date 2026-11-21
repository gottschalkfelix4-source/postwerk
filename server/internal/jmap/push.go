package jmap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"postwerk/internal/store"
)

// maxPushConnections bounds concurrent EventSource streams per account.
const maxPushConnections = 10

// pushTypes are the data types announced in StateChange events.
var pushTypes = []string{store.TypeMailbox, store.TypeEmail, store.TypeThread, store.TypeEmailDelivery, store.TypeEmailSubmission}

// handleEventSource streams StateChange events (RFC 8620 section 7.3).
func (s *Server) handleEventSource(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	release, ok := s.acquire("push", acct.ID, maxPushConnections)
	if !ok {
		s.problem(w, http.StatusTooManyRequests, "", "too many push connections")
		return
	}
	defer release()
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.problem(w, http.StatusInternalServerError, "", "streaming not supported")
		return
	}
	q := r.URL.Query()
	want := map[string]bool{}
	if t := q.Get("types"); t == "" || t == "*" {
		for _, typ := range pushTypes {
			want[typ] = true
		}
	} else {
		for _, typ := range strings.Split(t, ",") {
			want[strings.TrimSpace(typ)] = true
		}
	}
	closeAfterState := q.Get("closeafter") == "state"
	ping, _ := strconv.Atoi(q.Get("ping"))
	if ping > 0 && ping < 10 {
		ping = 10
	}
	if ping > 600 {
		ping = 600
	}

	// Subscribe before taking the snapshot, so no change can slip between.
	changes, unsubscribe := s.Store.SubscribeChanges(acct.ID)
	defer unsubscribe()
	last, err := s.Store.ChangeStates(r.Context(), acct.ID)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, "", "could not read state")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": postwerk jmap push\n\n")
	flusher.Flush()

	var pingC <-chan time.Time
	if ping > 0 {
		t := time.NewTicker(time.Duration(ping) * time.Second)
		defer t.Stop()
		pingC = t.C
	}
	accountID := fmtID(prefixAccount, acct.ID)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingC:
			if _, err := fmt.Fprintf(w, "event: ping\ndata: {\"interval\":%d}\n\n", ping); err != nil {
				return
			}
			flusher.Flush()
		case <-changes:
			cur, err := s.Store.ChangeStates(r.Context(), acct.ID)
			if err != nil {
				return
			}
			changed := map[string]string{}
			for typ, v := range cur {
				if want[typ] && v != last[typ] {
					changed[typ] = strconv.FormatInt(v, 10)
				}
			}
			last = cur
			if len(changed) == 0 {
				continue
			}
			data, _ := json.Marshal(map[string]any{"@type": "StateChange", "changed": map[string]any{accountID: changed}})
			if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
			if closeAfterState {
				return
			}
		}
	}
}
