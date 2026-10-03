// Package status exposes the orchestrator's passive operational status.
package status

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/state"
)

// Item identifies the most recently updated terminal workflow item.
type Item struct {
	GUID      string    `json:"guid"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Snapshot is the operator-facing state returned by /status.
type Snapshot struct {
	LastSuccessfulPoll   *time.Time `json:"lastSuccessfulPoll"`
	LastSuccessfulXCheck *time.Time `json:"lastSuccessfulCompleteXCheck"`
	Retry                int        `json:"retry"`
	Uncertain            int        `json:"uncertain"`
	LastMissed           *Item      `json:"lastMissed"`
	LastFailedTerminal   *Item      `json:"lastFailedTerminal"`
}

// Tracker synchronizes runtime and workflow summaries, replacing pointed-to values on update.
type Tracker struct {
	mu         sync.Mutex
	now        func() time.Time
	maxPollAge time.Duration
	snapshot   Snapshot
}

// New returns a tracker whose health expires after maxPollAge.
func New(maxPollAge time.Duration) *Tracker {
	return &Tracker{now: time.Now, maxPollAge: maxPollAge}
}

// RecordPoll records a fully successful poll.
func (t *Tracker) RecordPoll(at time.Time) {
	at = at.UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snapshot.LastSuccessfulPoll = &at
}

// RecordXCheck records a successful complete duplicate check.
func (t *Tracker) RecordXCheck(at time.Time) {
	at = at.UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snapshot.LastSuccessfulXCheck = &at
}

// Refresh rebuilds workflow counters and terminal-item summaries.
func (t *Tracker) Refresh(items map[string]state.Entry) {
	var retry, uncertain int
	var lastMissed, lastFailed *Item
	for guid := range items {
		entry := items[guid]
		switch entry.Status {
		case state.StatusRetry:
			retry++
		case state.StatusUncertain:
			uncertain++
		case state.StatusMissed:
			lastMissed = newerItem(lastMissed, guid, &entry)
		case state.StatusFailedTerminal:
			lastFailed = newerItem(lastFailed, guid, &entry)
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.snapshot.Retry = retry
	t.snapshot.Uncertain = uncertain
	t.snapshot.LastMissed = lastMissed
	t.snapshot.LastFailedTerminal = lastFailed
}

// Snapshot returns a shallow copy; callers may retain but must not mutate pointed-to values.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshot
}

// Handler returns passive health and status endpoints.
func (t *Tracker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", t.handleHealth)
	mux.HandleFunc("GET /status", t.handleStatus)
	return mux
}

func (t *Tracker) handleHealth(w http.ResponseWriter, _ *http.Request) {
	snapshot := t.Snapshot()
	healthy := snapshot.LastSuccessfulPoll != nil && t.now().Sub(*snapshot.LastSuccessfulPoll) <= t.maxPollAge
	statusCode := http.StatusOK
	if !healthy {
		statusCode = http.StatusServiceUnavailable
	}
	writeJSON(w, statusCode, map[string]any{
		"healthy":            healthy,
		"lastSuccessfulPoll": snapshot.LastSuccessfulPoll,
	})
}

func (t *Tracker) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, t.Snapshot())
}

func newerItem(current *Item, guid string, entry *state.Entry) *Item {
	if current != nil && !entry.UpdatedAt.After(current.UpdatedAt) {
		return current
	}
	return &Item{GUID: guid, Title: entry.Title, UpdatedAt: entry.UpdatedAt}
}

func writeJSON(w http.ResponseWriter, statusCode int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(body)
}
