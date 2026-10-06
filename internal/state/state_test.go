package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestSaveRoundTripsOldEntries(t *testing.T) {
	t.Parallel()
	old := time.Now().UTC().Add(-61 * 24 * time.Hour)
	want := Entry{
		Title: "Stored article", Link: "https://example.invalid/article",
		PostURL: "https://x.invalid/status/1", FoundOnX: true, Attempts: 3, LastError: "synthetic failure",
		PublishedAt: old, UpdatedAt: old, NextAttemptAt: old, ReplayRequestedAt: old,
	}
	statuses := []string{StatusSeeded, StatusPosted, StatusMissed, StatusFailedTerminal, StatusPosting, StatusRetry, StatusUncertain}
	raw := make([]string, 0, len(statuses))
	for _, status := range statuses {
		raw = append(raw, fmt.Sprintf(`"%[1]s": {
   "title": "Stored article",
   "link": "https://example.invalid/article",
   "status": "%[1]s",
   "post_url": "https://x.invalid/status/1",
   "found_on_x": true,
   "attempts": 3,
   "last_error": "synthetic failure",
   "published_at": "%[2]s",
   "updated_at": "%[2]s",
   "next_attempt_at": "%[2]s",
   "replay_requested_at": "%[2]s"
  }`, status, old.Format(time.RFC3339Nano)))
	}
	path := filepath.Join(t.TempDir(), "state.json")
	testutil.NoError(t, os.WriteFile(path, []byte(`{"items":{`+strings.Join(raw, ",")+`}}`), 0o600))
	store, err := Load(path)
	testutil.NoError(t, err)
	fresh, err := Load(filepath.Join(t.TempDir(), "state.json"))
	testutil.NoError(t, err)
	for _, status := range statuses {
		fresh.SetAt(status, &Entry{Status: status, PublishedAt: old}, old)
	}
	for _, tt := range []struct {
		name  string
		store *Store
		want  Entry
	}{
		{name: "loaded full entries", store: store, want: want},
		{name: "new minimal entries", store: fresh, want: Entry{PublishedAt: old, UpdatedAt: old}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testutil.NoError(t, tt.store.Save())
			reloaded, err := Load(tt.store.path)
			testutil.NoError(t, err)
			for _, snapshot := range []*Store{tt.store, reloaded} {
				testutil.Equal(t, len(snapshot.Items), len(statuses))
				for _, status := range statuses {
					tt.want.Status = status
					testutil.Equal(t, snapshot.Items[status], tt.want)
				}
			}
		})
	}
}

func TestLoadRejectsUnknownStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, entry, want string
	}{
		{name: "unknown", entry: `{"status":"mystery"}`, want: `"mystery"`},
		{name: "unknown format", entry: `{"status":"retry","format":"mystery"}`, want: `"mystery"`},
		{name: "null", entry: `null`, want: `""`},
		{name: "replaced post without URL", entry: `{"status":"posted","replaces":{"format":"image"}}`, want: "invalid replaced post"},
		{name: "replaced post with unknown format", entry: `{"status":"posted","replaces":{"post_url":"https://x.invalid/status/1","format":"mystery"}}`, want: "invalid replaced post"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "state.json")
			testutil.NoError(t, os.WriteFile(path, []byte(`{"items":{"guid-unknown":`+tt.entry+`}}`), 0o600))

			_, err := Load(path)
			if err == nil {
				t.Fatal("Load() error = nil, want unknown-status error")
			}
			if message := err.Error(); !strings.Contains(message, "guid-unknown") || !strings.Contains(message, tt.want) {
				t.Errorf("Load() error = %q, want GUID and status %s", message, tt.want)
			}
		})
	}
}

func TestDone(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{name: "missing", want: false},
		{name: "retry", status: StatusRetry, want: false},
		{name: "uncertain", status: StatusUncertain, want: false},
		{name: "posting", status: StatusPosting, want: true},
		{name: "posted", status: StatusPosted, want: true},
		{name: "seeded", status: StatusSeeded, want: true},
		{name: "missed", status: StatusMissed, want: true},
		{name: "failed terminal", status: StatusFailedTerminal, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &Store{Items: map[string]Entry{}}
			if tt.status != "" {
				store.Items["guid"] = Entry{Status: tt.status}
			}
			testutil.Equal(t, store.Done("guid"), tt.want)
		})
	}
}
