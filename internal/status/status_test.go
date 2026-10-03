package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/state"
	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestHealthTracksSuccessfulPollAge(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	tracker := New(6 * time.Minute)
	tracker.now = func() time.Time { return now }

	assertHealthStatus(t, tracker, http.StatusServiceUnavailable)
	tracker.RecordPoll(now.Add(-5 * time.Minute))
	assertHealthStatus(t, tracker, http.StatusOK)
	tracker.RecordPoll(now.Add(-7 * time.Minute))
	assertHealthStatus(t, tracker, http.StatusServiceUnavailable)
}

func TestStatusReportsWorkflowProjection(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	tracker := New(6 * time.Minute)
	tracker.RecordPoll(now)
	tracker.RecordXCheck(now.Add(-time.Minute))
	tracker.Refresh(map[string]state.Entry{
		"retry-1":    {Status: state.StatusRetry},
		"retry-2":    {Status: state.StatusRetry},
		"uncertain":  {Status: state.StatusUncertain},
		"missed-old": {Title: "Old", Status: state.StatusMissed, UpdatedAt: now.Add(-2 * time.Hour)},
		"missed-new": {Title: "New", Status: state.StatusMissed, UpdatedAt: now.Add(-time.Hour)},
		"failed":     {Title: "Failed", Status: state.StatusFailedTerminal, UpdatedAt: now.Add(-30 * time.Minute)},
	})

	recorder := httptest.NewRecorder()
	tracker.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", http.NoBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", recorder.Code)
	}
	var got Snapshot
	testutil.NoError(t, json.NewDecoder(recorder.Body).Decode(&got))
	if got.Retry != 2 || got.Uncertain != 1 {
		t.Errorf("counts = retry %d, uncertain %d", got.Retry, got.Uncertain)
	}
	if got.LastMissed == nil || got.LastMissed.GUID != "missed-new" {
		t.Errorf("LastMissed = %#v", got.LastMissed)
	}
	if got.LastFailedTerminal == nil || got.LastFailedTerminal.GUID != "failed" {
		t.Errorf("LastFailedTerminal = %#v", got.LastFailedTerminal)
	}
}

func assertHealthStatus(t *testing.T, tracker *Tracker, want int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	tracker.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", http.NoBody))
	testutil.Equal(t, recorder.Code, want)
}
