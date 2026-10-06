package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/oszuidwest/zw-xposter/internal/config"
	"github.com/oszuidwest/zw-xposter/internal/feed"
	"github.com/oszuidwest/zw-xposter/internal/poster"
	"github.com/oszuidwest/zw-xposter/internal/state"
	operational "github.com/oszuidwest/zw-xposter/internal/status"
	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

var testNow = time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)

type pollTestOptions struct {
	now       func() time.Time
	published time.Time
	initial   state.Entry
	// extra are additional feed items; their Link is a path on the test server.
	extra []feed.Item
	// video gives the article a featured video at /video.mp4 on the content server.
	video   bool
	recent  func(http.ResponseWriter, *http.Request, feed.Item)
	post    func(http.ResponseWriter, *http.Request, feed.Item)
	delete  func(http.ResponseWriter, *http.Request)
	content http.HandlerFunc
}

type pollTest struct {
	t           *testing.T
	app         *app
	item        feed.Item
	items       []feed.Item
	recentCalls atomic.Int32
	recentHours atomic.Int64
	postCalls   atomic.Int32
	deleteCalls atomic.Int32
	feedCalls   atomic.Int32
	heartbeats  atomic.Int32
}

func newPollTest(t *testing.T, opts *pollTestOptions) *pollTest {
	t.Helper()

	now := opts.now
	if now == nil {
		now = func() time.Time { return testNow }
	}
	published := opts.published
	if published.IsZero() {
		published = now().Add(-time.Hour)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")

	fixture := &pollTest{t: t}
	server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed":
			fixture.feedCalls.Add(1)
			writeFeed(t, w, fixture.items)
		case "/heartbeat":
			fixture.heartbeats.Add(1)
		case "/recent":
			fixture.recentCalls.Add(1)
			hours, err := strconv.Atoi(r.URL.Query().Get("hours"))
			testutil.Equal(t, err, nil)
			fixture.recentHours.Store(int64(hours))
			if opts.recent != nil {
				opts.recent(w, r, fixture.item)
				return
			}
			// Like poster/server.mjs, refuse more history than it can read.
			if maxHours := poster.MaxLookbackHours; hours > maxHours {
				testutil.JSON(t, w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("hours must be between 0 and %d", maxHours)})
				return
			}
			testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": []any{}, "complete": true})
		case "/post", "/post-video":
			fixture.postCalls.Add(1)
			if opts.post != nil {
				opts.post(w, r, fixture.item)
				return
			}
			testutil.JSON(t, w, http.StatusOK, map[string]any{"url": "https://x.invalid/fixture/status/1"})
		case "/delete":
			fixture.deleteCalls.Add(1)
			serveDelete(t, w, r, opts.delete)
		default:
			if opts.content != nil {
				opts.content(w, r)
				return
			}
			// Any other path is an article page.
			w.Header().Set("Content-Type", "text/html")
			if _, err := w.Write([]byte("<html><head></head><body>synthetic article</body></html>")); err != nil {
				t.Errorf("write article response: %v", err)
			}
		}
	})

	store, err := state.Load(statePath)
	testutil.NoError(t, err)
	item := feed.Item{
		GUID:      "guid-1",
		Title:     "Synthetic article",
		Link:      server.URL + "/article",
		Published: published,
	}
	fixture.item = item
	fixture.items = []feed.Item{item}
	if opts.video {
		fixture.items[0].VideoURL = server.URL + "/video.mp4"
	}
	for _, extra := range opts.extra {
		extra.Link = server.URL + extra.Link
		fixture.items = append(fixture.items, extra)
	}
	if opts.initial.Status != "" {
		entry := opts.initial
		if entry.Title == "" {
			entry.Title = item.Title
		}
		if entry.Link == "" {
			entry.Link = item.Link
		}
		if entry.PublishedAt.IsZero() {
			entry.PublishedAt = item.Published
		}
		updatedAt := entry.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = now().Add(-time.Minute)
		}
		store.SetAt(item.GUID, &entry, updatedAt)
	}

	fixture.app = &app{
		cfg: &config.Config{
			FeedURL:            server.URL + "/feed",
			PosterURL:          server.URL,
			StateFile:          statePath,
			PostDelay:          0,
			MaxAge:             24 * time.Hour,
			VideoReplaceWindow: 6 * time.Hour,
		},
		store:  store,
		http:   server.Client(),
		poster: poster.New(server.URL),
		now:    now,
		status: operational.New(10 * time.Minute),
	}
	return fixture
}

// serveDelete confirms deletions unless the test handles them.
func serveDelete(t *testing.T, w http.ResponseWriter, r *http.Request, handler func(http.ResponseWriter, *http.Request)) {
	if handler != nil {
		handler(w, r)
		return
	}
	var payload struct{ ID string }
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Error(err)
	}
	testutil.JSON(t, w, http.StatusOK, map[string]string{"deleted": payload.ID})
}

// poll requires a successful workflow run and returns the primary item's state.
func (f *pollTest) poll() state.Entry {
	f.t.Helper()
	if err := f.app.poll(f.t.Context()); err != nil {
		f.t.Fatalf("poll() error = %v", err)
	}
	return f.app.store.Items[f.item.GUID]
}

func (f *pollTest) assertCalls(recent, posts int32) {
	f.t.Helper()
	if got := f.recentCalls.Load(); got != recent {
		f.t.Errorf("recent calls = %d, want %d", got, recent)
	}
	if got := f.postCalls.Load(); got != posts {
		f.t.Errorf("POST calls = %d, want %d", got, posts)
	}
}

func (f *pollTest) assertPollFailsUnchanged(wantErr string, recent int32) {
	f.t.Helper()
	before := maps.Clone(f.app.store.Items)
	testutil.ErrorContains(f.t, f.app.poll(f.t.Context()), wantErr)
	f.assertCalls(recent, 0)
	if !maps.Equal(f.app.store.Items, before) {
		f.t.Errorf("state changed after failed poll\nbefore: %#v\nafter:  %#v", before, f.app.store.Items)
	}
}

func writeFeed(t *testing.T, w http.ResponseWriter, items []feed.Item) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss><channel>`)
	for _, item := range items {
		fmt.Fprintf(&b, `<item><title>%s</title><link>%s</link><guid>%s</guid><pubDate>%s</pubDate>`,
			item.Title, item.Link, item.GUID, item.Published.Format(time.RFC1123Z))
		if item.VideoURL != "" {
			fmt.Fprintf(&b, `<enclosure url="%s" type="video/mp4" length="123"/>`, html.EscapeString(item.VideoURL))
		}
		b.WriteString(`</item>`)
	}
	b.WriteString(`</channel></rss>`)
	w.Header().Set("Content-Type", "application/rss+xml")
	if _, err := io.WriteString(w, b.String()); err != nil {
		t.Errorf("write feed response: %v", err)
	}
}

func reloadPollState(t *testing.T, fixture *pollTest) *state.Store {
	t.Helper()
	saved, err := state.Load(fixture.app.cfg.StateFile)
	testutil.NoError(t, err)
	return saved
}

// blockStateSave makes writes fail even when tests run as root.
func blockStateSave(t *testing.T, path string) {
	t.Helper()
	testutil.NoError(t, os.Mkdir(path+".tmp", 0o700))
}

var maxLookbackHours = fmt.Sprintf("%d hours", poster.MaxLookbackHours)

func TestPostText(t *testing.T) {
	link := "https://www.zuidwestupdate.nl/news/article/"

	got := postText(&feed.Item{Title: "Short title", Link: link})
	testutil.Equal(t, got, "Short title "+link)

	long := strings.Repeat("é", 300)
	got = postText(&feed.Item{Title: long, Link: link})
	title := strings.TrimSuffix(got, " "+link)
	testutil.Equal(t, utf8.RuneCountInString(title), 256)
	testutil.Equal(t, strings.HasSuffix(title, "…"), true)
}

func TestPollPostTransitions(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		clicked     bool
		wantStatus  string
		wantBackoff time.Duration
	}{
		{name: "success becomes posted", statusCode: http.StatusOK, wantStatus: state.StatusPosted},
		{name: "rate limit before click becomes retry", statusCode: http.StatusTooManyRequests, wantStatus: state.StatusRetry, wantBackoff: retryInitial},
		{name: "server error before click becomes retry", statusCode: http.StatusInternalServerError, wantStatus: state.StatusRetry, wantBackoff: retryInitial},
		{name: "server error after click becomes uncertain", statusCode: http.StatusInternalServerError, clicked: true, wantStatus: state.StatusUncertain, wantBackoff: uncertainMinimum},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newPollTest(t, &pollTestOptions{
				post: func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
					if tt.statusCode == http.StatusOK {
						testutil.JSON(t, w, tt.statusCode, map[string]any{"url": "https://x.invalid/fixture/status/1"})
						return
					}
					testutil.JSON(t, w, tt.statusCode, map[string]any{"error": "synthetic poster failure", "clicked": tt.clicked})
				},
			})

			entry := fixture.poll()
			checkedAt := fixture.app.status.Snapshot().LastSuccessfulXCheck
			if checkedAt == nil || !checkedAt.Equal(testNow) {
				t.Errorf("last complete X check = %v, want %s", checkedAt, testNow)
			}
			testutil.Equal(t, entry.Status, tt.wantStatus)
			testutil.Equal(t, entry.Attempts, 1)
			wantNext := time.Time{}
			if tt.wantBackoff > 0 {
				wantNext = testNow.Add(tt.wantBackoff)
			}
			testutil.Equal(t, entry.NextAttemptAt, wantNext)
		})
	}
}

func TestPollLostResponseIsReconciledWithoutSecondPost(t *testing.T) {
	var clock atomic.Int64
	clock.Store(testNow.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	var posted atomic.Bool
	fixture := newPollTest(t, &pollTestOptions{
		now: now,
		recent: func(w http.ResponseWriter, _ *http.Request, item feed.Item) {
			posts := []any{}
			if posted.Load() {
				posts = append(posts, map[string]any{
					"id":        "900000000000000001",
					"url":       "https://x.invalid/fixture/status/900000000000000001",
					"text":      "Synthetic article " + item.Link,
					"urls":      []string{item.Link},
					"createdAt": now().Format(time.RFC3339),
				})
			}
			testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": posts, "complete": true})
		},
		post: func(_ http.ResponseWriter, _ *http.Request, _ feed.Item) {
			posted.Store(true)
			panic(http.ErrAbortHandler)
		},
	})

	fixture.poll()
	if got := fixture.app.store.Items[fixture.item.GUID].Status; got != state.StatusUncertain {
		t.Fatalf("status after lost response = %q, want %q", got, state.StatusUncertain)
	}

	clock.Store(testNow.Add(uncertainMinimum).UnixNano())
	entry := fixture.poll()
	if entry.Status != state.StatusPosted || !entry.FoundOnX {
		t.Errorf("reconciled entry = %#v, want posted and found_on_x", entry)
	}
	testutil.Equal(t, fixture.postCalls.Load(), 1)
}

func TestPollIncompleteRecentFailsClosed(t *testing.T) {
	for _, initial := range []state.Entry{
		{},
		{Status: state.StatusRetry, Attempts: 2, LastError: "try later", UpdatedAt: testNow.Add(-time.Hour)},
	} {
		name := initial.Status
		if name == "" {
			name = "new item"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newPollTest(t, &pollTestOptions{
				initial: initial,
				recent: func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
					testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": []any{}, "complete": false})
				},
			})
			fixture.assertPollFailsUnchanged("incomplete", 1)
		})
	}
}

func TestPingHeartbeat(t *testing.T) {
	var calls atomic.Int32
	server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		testutil.Equal(t, r.Method, http.MethodGet)
		w.WriteHeader(http.StatusNoContent)
	})

	a := &app{
		cfg: &config.Config{HeartbeatURL: server.URL},
	}
	a.pingHeartbeat(t.Context())
	testutil.Equal(t, calls.Load(), 1)
}

func TestPollCancellationDuringPostBecomesUncertain(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release := make(chan struct{})
	fixture := newPollTest(t, &pollTestOptions{
		post: func(_ http.ResponseWriter, _ *http.Request, _ feed.Item) {
			cancel()
			<-release
		},
	})
	t.Cleanup(func() { close(release) })
	testutil.NoError(t, fixture.app.poll(ctx))
	entry := reloadPollState(t, fixture).Items[fixture.item.GUID]
	if fixture.postCalls.Load() != 1 || entry.Status != state.StatusUncertain || !strings.Contains(entry.LastError, "context canceled") {
		t.Errorf("entry after cancellation = %#v, POST calls = %d", entry, fixture.postCalls.Load())
	}
}

func TestPollDoesNotPostWhenWriteAheadSaveFails(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{initial: state.Entry{Status: state.StatusRetry, Format: "text"}})
	blockStateSave(t, fixture.app.cfg.StateFile)

	fixture.assertPollFailsUnchanged("write-ahead", 1)
}

func TestPollExpiredTransitions(t *testing.T) {
	tests := []struct {
		name              string
		initial           state.Entry
		wantStatus        string
		wantErrorContains string
	}{
		{name: "pending becomes missed", wantStatus: state.StatusMissed},
		{
			name: "retry becomes failed terminal",
			initial: state.Entry{
				Status:    state.StatusRetry,
				Attempts:  2,
				LastError: "synthetic failure",
			},
			wantStatus: state.StatusFailedTerminal,
		},
		{
			name: "uncertain warns that the post may be on X",
			initial: state.Entry{
				Status:    state.StatusUncertain,
				Attempts:  1,
				LastError: "synthetic lost response",
			},
			wantStatus:        state.StatusFailedTerminal,
			wantErrorContains: "may already be on X and must be checked manually",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newPollTest(t, &pollTestOptions{
				published: testNow.Add(-25 * time.Hour),
				initial:   tt.initial,
			})
			entry := fixture.poll()
			testutil.Equal(t, entry.Status, tt.wantStatus)
			if !strings.Contains(entry.LastError, tt.wantErrorContains) {
				t.Errorf("last error = %q, want it to contain %q", entry.LastError, tt.wantErrorContains)
			}
			fixture.assertCalls(0, 0)
		})
	}
}

// addUnlisted stores an article outside the feed and returns its stored value.
func addUnlisted(fixture *pollTest, entry *state.Entry, updatedAt time.Time) state.Entry {
	const guid = "guid-unlisted"
	entry.Title = "Unlisted " + guid
	entry.Link = "https://example.invalid/" + guid
	fixture.app.store.SetAt(guid, entry, updatedAt)
	return fixture.app.store.Items[guid]
}

func TestPollExpiresUnlistedPendingItems(t *testing.T) {
	expired := testNow.Add(-48 * time.Hour)
	tests := []struct {
		name              string
		entry             state.Entry
		wantStatus        string // empty when the entry must not change
		wantErrorContains string
	}{
		{name: "retry", entry: state.Entry{Status: state.StatusRetry, PublishedAt: expired}, wantStatus: state.StatusFailedTerminal},
		{
			name:              "uncertain",
			entry:             state.Entry{Status: state.StatusUncertain, PublishedAt: expired},
			wantStatus:        state.StatusFailedTerminal,
			wantErrorContains: "may already be on X and must be checked manually",
		},
		// Without a publication date the age is unknown, so it cannot be within MAX_AGE.
		{name: "retry without published_at", entry: state.Entry{Status: state.StatusRetry}, wantStatus: state.StatusFailedTerminal},
		{name: "fresh retry", entry: state.Entry{Status: state.StatusRetry, PublishedAt: testNow.Add(-time.Hour)}},
		{name: "fresh uncertain", entry: state.Entry{Status: state.StatusUncertain, PublishedAt: testNow.Add(-time.Hour)}},
		{
			name:  "replay within MAX_AGE",
			entry: state.Entry{Status: state.StatusRetry, PublishedAt: testNow.Add(-72 * time.Hour), ReplayRequestedAt: testNow.Add(-time.Hour)},
		},
		{name: "posted", entry: state.Entry{Status: state.StatusPosted, PublishedAt: expired}},
		{name: "seeded", entry: state.Entry{Status: state.StatusSeeded, PublishedAt: expired}},
		{name: "missed", entry: state.Entry{Status: state.StatusMissed, PublishedAt: expired}},
		{name: "failed terminal", entry: state.Entry{Status: state.StatusFailedTerminal, PublishedAt: expired}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newPollTest(t, &pollTestOptions{})
			fixture.items = nil
			entry := tt.entry
			entry.Attempts = 2
			entry.LastError = "synthetic failure"
			if entry.Status == state.StatusRetry || entry.Status == state.StatusUncertain {
				entry.NextAttemptAt = testNow.Add(-time.Minute)
			}
			before := addUnlisted(fixture, &entry, testNow.Add(-time.Hour))
			unchanged := maps.Clone(fixture.app.store.Items)

			fixture.poll()
			fixture.assertCalls(0, 0)
			if tt.wantStatus == "" {
				if got := fixture.app.store.Items; !maps.Equal(got, unchanged) {
					t.Errorf("state changed\nbefore: %#v\nafter:  %#v", unchanged, got)
				}
				return
			}

			reloaded := reloadPollState(t, fixture)
			saved := reloaded.Items["guid-unlisted"]
			if saved.Status != tt.wantStatus {
				t.Fatalf("saved entry = %#v, want status %q", saved, tt.wantStatus)
			}
			if !strings.Contains(saved.LastError, tt.wantErrorContains) || !strings.Contains(saved.LastError, "synthetic failure") {
				t.Errorf("last error = %q, want %q and the previous error", saved.LastError, tt.wantErrorContains)
			}
			if saved.Title != before.Title || saved.Link != before.Link || saved.Attempts != before.Attempts {
				t.Errorf("article fields changed: %#v", saved)
			}
			testutil.Equal(t, saved.NextAttemptAt, time.Time{})
			if _, ok := workflowAlert("guid-unlisted", &before, &saved); !ok {
				t.Error("transition does not raise a workflow alert")
			}
		})
	}
}

func TestPollPreservesOldFinalEntries(t *testing.T) {
	for _, status := range []string{state.StatusSeeded, state.StatusPosted, state.StatusMissed, state.StatusFailedTerminal} {
		t.Run(status, func(t *testing.T) {
			now := time.Now().UTC()
			old := now.Add(-61 * 24 * time.Hour)
			fixture := newPollTest(t, &pollTestOptions{
				now:       func() time.Time { return now },
				published: old,
				initial:   state.Entry{Status: status, UpdatedAt: old},
			})
			before := fixture.app.store.Items[fixture.item.GUID]
			// Expiring an unlisted retry forces a save before the listed item is checked.
			addUnlisted(fixture, &state.Entry{Status: state.StatusRetry, PublishedAt: old}, old)

			fixture.poll()
			reloaded := reloadPollState(t, fixture)
			if got := reloaded.Items["guid-unlisted"].Status; got != state.StatusFailedTerminal {
				t.Fatalf("saved unlisted status = %q, want failed_terminal", got)
			}
			for _, store := range []*state.Store{fixture.app.store, reloaded} {
				entry := store.Items[fixture.item.GUID]
				testutil.Equal(t, entry, before)
				if _, ok := workflowAlert(fixture.item.GUID, &before, &entry); ok {
					t.Error("old final entry raises a workflow alert")
				}
			}
			fixture.assertCalls(0, 0)
		})
	}
}

func TestPollKeepsUnlistedItemsWhenFeedFetchFails(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{})
	failing := testutil.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	fixture.app.cfg.FeedURL = failing.URL
	addUnlisted(fixture, &state.Entry{Status: state.StatusRetry, PublishedAt: testNow.Add(-48 * time.Hour)}, testNow.Add(-time.Hour))
	fixture.assertPollFailsUnchanged("fetch feed", 0)
}

func TestPollExpiresUnlistedItemsWhenDuplicateCheckFails(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{
		recent: func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
			testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": []any{}, "complete": false})
		},
	})
	addUnlisted(fixture, &state.Entry{Status: state.StatusUncertain, PublishedAt: testNow.Add(-48 * time.Hour)}, testNow.Add(-time.Hour))

	testutil.ErrorContains(t, fixture.app.poll(t.Context()), "incomplete")
	reloaded := reloadPollState(t, fixture)
	testutil.Equal(t, reloaded.Items["guid-unlisted"].Status, state.StatusFailedTerminal)
	if entry, ok := fixture.app.store.Items[fixture.item.GUID]; ok {
		t.Errorf("current item = %#v, want it untouched", entry)
	}
	fixture.assertCalls(1, 0)
}

func TestPollRollsBackUnlistedExpiryWhenSaveFails(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{})
	addUnlisted(fixture, &state.Entry{Status: state.StatusRetry, PublishedAt: testNow.Add(-48 * time.Hour)}, testNow.Add(-time.Hour))
	blockStateSave(t, fixture.app.cfg.StateFile)

	fixture.assertPollFailsUnchanged("state.json.tmp", 0)
}

func TestLoadStore(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "state.json")
	existing := filepath.Join(t.TempDir(), "state.json")
	testutil.NoError(t, os.WriteFile(existing, []byte(`{"items":{}}`), 0o600))
	corrupt := filepath.Join(t.TempDir(), "state.json")
	testutil.NoError(t, os.WriteFile(corrupt, []byte("not JSON"), 0o600))

	tests := []struct {
		name    string
		path    string
		seed    bool
		wantErr string
	}{
		{name: "missing without seed", path: missing, wantErr: "-seed"},
		{name: "missing with seed", path: missing, seed: true},
		{name: "existing without seed", path: existing},
		{name: "existing with seed", path: existing, seed: true, wantErr: "already exists"},
		{name: "corrupt", path: corrupt, seed: true, wantErr: "parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := loadStore(tt.path, tt.seed)
			if tt.wantErr != "" {
				testutil.ErrorContains(t, err, tt.wantErr)
				return
			}
			testutil.NoError(t, err)
			testutil.Equal(t, store.Exists(), !tt.seed)
		})
	}
}

func TestPollDryRunPreviewsOnce(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{})
	fixture.app.cfg.DryRun = true

	for range 2 {
		fixture.poll()
	}
	fixture.assertCalls(1, 0)
	reloaded := reloadPollState(t, fixture)
	if _, ok := reloaded.Items[fixture.item.GUID]; ok {
		t.Error("dry run saved the previewed item to the state file")
	}
}

func TestNewAlertsDisabledDuringDryRun(t *testing.T) {
	cfg := &config.Config{
		DryRun:        true,
		AlertReminder: time.Hour,
		Graph: config.GraphConfig{
			TenantID:     "tenant",
			ClientID:     "client",
			ClientSecret: "secret",
			FromAddress:  "sender@example.com",
			Recipients:   []string{"ops@example.com"},
		},
	}
	alerts := newAlerts(cfg)
	defer alerts.Close()
	if alerts.IsConfigured() {
		t.Fatal("dry run enabled alert e-mail")
	}
}

func TestWorkflowAlertOnlyOnTerminalTransition(t *testing.T) {
	retry := state.Entry{Status: state.StatusRetry}
	missed := state.Entry{Title: "Missed article", Status: state.StatusMissed}
	failed := state.Entry{Title: "Failed article", Status: state.StatusFailedTerminal, LastError: "boom"}
	posted := state.Entry{Status: state.StatusPosted}

	tests := []struct {
		name              string
		previous, current state.Entry
		want              bool
	}{
		{name: "new item missed", current: missed, want: true},
		{name: "retry failed permanently", previous: retry, current: failed, want: true},
		{name: "missed stays missed", previous: missed, current: missed},
		{name: "replayed item posted", previous: missed, current: posted},
		{name: "retry posted", previous: retry, current: posted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, got := workflowAlert("guid-1", &tt.previous, &tt.current)
			if got != tt.want {
				t.Fatalf("workflowAlert() = %v, want %v", got, tt.want)
			}
			if got && event.Key != "workflow:"+tt.current.Status+":guid-1" {
				t.Errorf("event key = %q", event.Key)
			}
		})
	}
}

func TestRecordClearsScheduleOnlyForFinalStatus(t *testing.T) {
	tests := []struct {
		status    string
		wantClear bool
	}{
		{status: state.StatusPosted, wantClear: true},
		{status: state.StatusMissed, wantClear: true},
		{status: state.StatusFailedTerminal, wantClear: true},
		{status: state.StatusPosting},
		{status: state.StatusRetry},
		{status: state.StatusUncertain},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			fixture := newPollTest(t, &pollTestOptions{})
			entry := state.Entry{
				Status:            tt.status,
				NextAttemptAt:     testNow.Add(time.Minute),
				ReplayRequestedAt: testNow.Add(-time.Minute),
			}
			want := entry
			want.UpdatedAt = testNow
			if tt.wantClear {
				want.NextAttemptAt = time.Time{}
				want.ReplayRequestedAt = time.Time{}
			}
			testutil.NoError(t, fixture.app.record(fixture.item.GUID, &entry))
			testutil.Equal(t, reloadPollState(t, fixture).Items[fixture.item.GUID], want)
		})
	}
}

func TestSeedPersistsCurrentFeedWithoutPosting(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{})

	testutil.NoError(t, fixture.app.seed(t.Context()))
	entry := fixture.app.store.Items[fixture.item.GUID]
	testutil.Equal(t, entry.Status, state.StatusSeeded)
	fixture.assertCalls(0, 0)
	reloaded := reloadPollState(t, fixture)
	if !reloaded.Exists() || reloaded.Items[fixture.item.GUID].Status != state.StatusSeeded {
		t.Errorf("persisted seed = %#v", reloaded.Items[fixture.item.GUID])
	}
}

func TestReplayResetsEntryAndExtendsLookback(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{
		published: testNow.Add(-72 * time.Hour),
		initial: state.Entry{
			Status:   state.StatusPosted,
			PostURL:  "https://x.invalid/fixture/status/old",
			FoundOnX: true,
			Attempts: 3,
		},
	})

	testutil.NoError(t, fixture.app.replay(t.Context(), fixture.item.GUID))
	entry := fixture.app.store.Items[fixture.item.GUID]
	if entry.Status != state.StatusRetry || entry.PostURL != "" || entry.FoundOnX || entry.Attempts != 0 {
		t.Errorf("replayed entry = %#v", entry)
	}
	if entry.Title != fixture.item.Title || entry.Link != fixture.item.Link || !entry.PublishedAt.Equal(fixture.item.Published) {
		t.Errorf("replay did not keep the article metadata: %#v", entry)
	}
	testutil.Equal(t, fixture.app.expired(&entry, testNow), false)
	if got := fixture.app.recentLookback([]feed.Item{fixture.item}, testNow); got < 72*time.Hour {
		t.Errorf("lookback = %s, want at least 72h", got)
	}
}

func TestReplayRejectsWithoutChangingState(t *testing.T) {
	tests := []struct {
		name      string
		published time.Time
		guid      string
		wantErr   string
	}{
		{name: "GUID outside current feed", guid: "guid-not-in-feed", wantErr: "current feed"},
		{name: "article beyond poster lookback", published: testNow.Add(-poster.MaxLookback - time.Minute), guid: "guid-1", wantErr: maxLookbackHours},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newPollTest(t, &pollTestOptions{published: tt.published})
			fixture.app.store.SetAt(tt.guid, &state.Entry{
				Title:    "Stored article",
				Link:     "https://example.invalid/stored",
				Status:   state.StatusPosted,
				PostURL:  "https://x.invalid/fixture/status/stored",
				Attempts: 2,
			}, testNow.Add(-time.Hour))
			before := maps.Clone(fixture.app.store.Items)

			err := fixture.app.replay(t.Context(), tt.guid)
			testutil.ErrorContains(t, err, tt.wantErr)
			if after := fixture.app.store.Items; !maps.Equal(after, before) {
				t.Errorf("state changed after rejected replay\nbefore: %#v\nafter:  %#v", before, after)
			}
		})
	}
}

func TestPollIsolatesReplayBeyondPosterLookback(t *testing.T) {
	old := feed.Item{
		GUID:      "guid-old-replay",
		Title:     "Old replay",
		Link:      "/old",
		Published: testNow.Add(-poster.MaxLookback - time.Minute),
	}
	fixture := newPollTest(t, &pollTestOptions{extra: []feed.Item{old}})
	fixture.app.store.SetAt(old.GUID, &state.Entry{
		Title:             old.Title,
		Status:            state.StatusRetry,
		ReplayRequestedAt: testNow.Add(-time.Hour),
	}, testNow.Add(-time.Hour))

	fixture.poll()
	oldEntry := fixture.app.store.Items[old.GUID]
	if oldEntry.Status != state.StatusFailedTerminal || !strings.Contains(oldEntry.LastError, maxLookbackHours) {
		t.Errorf("old replay entry = %#v, want failed_terminal with lookback error", oldEntry)
	}
	testutil.Equal(t, fixture.app.store.Items[fixture.item.GUID].Status, state.StatusPosted)
	fixture.assertCalls(1, 1)
	// The uncheckable replay must not widen the check for the other items.
	testutil.Equal(t, fixture.recentHours.Load(), int64(poster.LookbackHours(fixture.app.cfg.MaxAge+recentMargin)))
}

// Even if startup validation is bypassed, an oversized lookback must fail closed.
func TestPollNeverShortensLookbackBelowMaxAge(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{published: testNow.Add(-400 * time.Hour)})
	fixture.app.cfg.MaxAge = 720 * time.Hour

	err := fixture.app.poll(t.Context())
	testutil.ErrorContains(t, err, "check X")
	testutil.Equal(t, fixture.recentHours.Load(), 744)
	testutil.Equal(t, fixture.postCalls.Load(), 0)
	if entry, ok := fixture.app.store.Items[fixture.item.GUID]; ok {
		t.Errorf("state entry = %#v, want nil", entry)
	}
}

func TestPollIgnoresReplayMarkerOnFinalEntry(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{
		published: testNow.Add(-200 * time.Hour),
		initial: state.Entry{
			Status:            state.StatusPosted,
			ReplayRequestedAt: testNow.Add(-time.Hour),
		},
		extra: []feed.Item{{GUID: "guid-2", Title: "New article", Link: "/new", Published: testNow.Add(-time.Hour)}},
	})

	fixture.poll()
	testutil.Equal(t, fixture.recentHours.Load(), 48)
}

func TestRunRejectsMaxAgeBeyondPosterLookback(t *testing.T) {
	tests := []struct {
		maxAge  string
		wantErr string
	}{
		{maxAge: "313h", wantErr: "MAX_AGE must be at most 312h"},
		{maxAge: "720h", wantErr: "MAX_AGE must be at most 312h"},
		// MaxAge + recentMargin would overflow time.Duration here.
		{maxAge: "2562047h", wantErr: "MAX_AGE must be at most 312h"},
		// The limit itself passes validation and stops at the missing state.
		{maxAge: "312h", wantErr: "-seed"},
	}
	for _, tt := range tests {
		t.Run(tt.maxAge, func(t *testing.T) {
			t.Setenv("MAX_AGE", tt.maxAge)
			t.Setenv("STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
			t.Setenv("FEED_URL", "https://127.0.0.1:1/feed")
			t.Setenv("POSTER_URL", "http://127.0.0.1:1")

			err := run(options{once: true}, nil)
			testutil.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestRunDryRunDoesNotWriteState(t *testing.T) {
	// A regression must never reach real mail endpoints.
	for _, key := range []string{"GRAPH_TENANT_ID", "GRAPH_CLIENT_ID", "GRAPH_CLIENT_SECRET", "GRAPH_FROM_ADDRESS", "ALERT_RECIPIENTS"} {
		t.Setenv(key, "")
	}
	published := time.Now().Add(-time.Hour)
	postingState := fmt.Sprintf(`{"items":{"guid-1":{"title":"Synthetic article","link":"https://example.invalid/article","status":"posting","attempts":1,"updated_at":%q}}}`,
		published.Format(time.RFC3339Nano))
	seededState := fmt.Sprintf(`{"items":{"guid-1":{"title":"Synthetic article","link":"https://example.invalid/article","status":"posted","post_url":"https://x.invalid/fixture/status/1","updated_at":%q}}}`,
		published.Format(time.RFC3339Nano))
	unlistedState := fmt.Sprintf(`{"items":{"guid-unlisted":{"title":"Unlisted","link":"https://example.invalid/unlisted","status":"retry","attempts":1,"published_at":%q,"updated_at":%q}}}`,
		published.Add(-48*time.Hour).Format(time.RFC3339Nano), published.Format(time.RFC3339Nano))

	tests := []struct {
		name      string
		state     string // empty means no state file
		emptyFeed bool
		opts      options
	}{
		{name: "empty feed", state: `{"items":{}}`, emptyFeed: true, opts: options{once: true}},
		{name: "poll", state: postingState, opts: options{once: true}},
		{name: "poll expiring an unlisted item", state: unlistedState, opts: options{once: true}},
		{name: "seed", opts: options{seed: true}},
		{name: "replay", state: seededState, opts: options{replay: "guid-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newPollTest(t, &pollTestOptions{published: published})
			if tt.emptyFeed {
				fixture.items = nil
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			if tt.state != "" {
				testutil.NoError(t, os.WriteFile(path, []byte(tt.state), 0o600))
			}
			t.Setenv("DRY_RUN", "true")
			t.Setenv("STATE_FILE", path)
			t.Setenv("FEED_URL", fixture.app.cfg.FeedURL)
			t.Setenv("POSTER_URL", fixture.app.cfg.PosterURL)
			t.Setenv("HEARTBEAT_URL", fixture.app.cfg.PosterURL+"/heartbeat")

			testutil.NoError(t, run(tt.opts, fixture.app.http))
			testutil.Equal(t, fixture.feedCalls.Load(), 1)
			testutil.Equal(t, fixture.heartbeats.Load(), 0)
			testutil.Equal(t, fixture.postCalls.Load(), 0)
			files, err := os.ReadDir(dir)
			testutil.NoError(t, err)
			if tt.state == "" {
				if len(files) != 1 || files[0].Name() != "state.json.lock" {
					t.Errorf("dry run created %v, want only the instance lock", files)
				}
				return
			}
			if len(files) != 2 || files[0].Name() != "state.json" || files[1].Name() != "state.json.lock" {
				t.Errorf("dry run left %v, want only state.json and its instance lock", files)
			}
			data, err := os.ReadFile(path) //nolint:gosec // The path is a test temp file.
			testutil.NoError(t, err)
			testutil.Equal(t, string(data), tt.state)
		})
	}
}

func TestRunOnceReturnsPollError(t *testing.T) {
	for _, tt := range []struct {
		name    string
		feed    string
		recent  bool
		wantErr string
	}{
		{name: "success", recent: true},
		{name: "feed failure", feed: "/unavailable", wantErr: "fetch feed"},
		{name: "incomplete timeline", wantErr: "incomplete"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newPollTest(t, &pollTestOptions{
				published: time.Now().Add(-time.Hour),
				content: func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
				},
				recent: func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
					testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": []any{}, "complete": tt.recent})
				},
			})
			testutil.NoError(t, fixture.app.store.Save())
			t.Setenv("DRY_RUN", "true")
			t.Setenv("STATE_FILE", fixture.app.cfg.StateFile)
			t.Setenv("FEED_URL", fixture.app.cfg.FeedURL+tt.feed)
			t.Setenv("POSTER_URL", fixture.app.cfg.PosterURL)

			testutil.ErrorContains(t, run(options{once: true}, fixture.app.http), tt.wantErr)
			testutil.Equal(t, fixture.postCalls.Load(), 0)
		})
	}
}

func TestRetryDelay(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 1, want: 2 * time.Minute},
		{attempts: 2, want: 4 * time.Minute},
		{attempts: 3, want: 8 * time.Minute},
		{attempts: 4, want: 16 * time.Minute},
		{attempts: 5, want: 30 * time.Minute},
		{attempts: 20, want: 30 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("attempt-%d", tt.attempts), func(t *testing.T) {
			testutil.Equal(t, retryDelay(tt.attempts), tt.want)
		})
	}
}
