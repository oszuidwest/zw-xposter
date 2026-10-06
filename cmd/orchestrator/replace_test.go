package main

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/feed"
	"github.com/oszuidwest/zw-xposter/internal/state"
	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

const (
	replacedURL = "https://x.invalid/fixture/status/100"
	videoURL    = "https://x.invalid/fixture/status/200"
)

func postedWithoutVideo() state.Entry {
	return state.Entry{Status: state.StatusPosted, PostURL: replacedURL, Format: state.FormatImage, AwaitingVideo: true, Attempts: 1}
}

func pendingDeletion() state.Entry {
	return state.Entry{
		Status: state.StatusPosted, PostURL: videoURL, Format: state.FormatVideoCaptions,
		Replaces: &state.Replacement{PostURL: replacedURL, Format: state.FormatImage, Attempts: 1},
	}
}

// serveTimeline lists the posts in onX, each linking to the article.
func serveTimeline(t *testing.T, onX *[]string) func(http.ResponseWriter, *http.Request, feed.Item) {
	return func(w http.ResponseWriter, _ *http.Request, item feed.Item) {
		posts := []any{}
		for _, url := range *onX {
			posts = append(posts, map[string]any{"url": url, "urls": []string{item.Link}})
		}
		testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": posts, "complete": true})
	}
}

// savedEntry reads the state file from a handler, where t.Fatal is not allowed.
func savedEntry(t *testing.T, f *pollTest) state.Entry {
	saved, err := state.Load(f.app.cfg.StateFile)
	if err != nil {
		t.Error(err)
		return state.Entry{}
	}
	return saved.Items[f.item.GUID]
}

// assertSaved compares the persisted outcome; reloaded timestamps differ only in location.
func assertSaved(t *testing.T, f *pollTest, want *state.Entry) {
	t.Helper()
	got := reloadPollState(t, f).Items[f.item.GUID]
	got.PublishedAt, got.UpdatedAt = want.PublishedAt, want.UpdatedAt
	testutil.Equal(t, got, *want)
}

func TestPollReplacesPostWhenVideoIsAdded(t *testing.T) {
	onX := []string{replacedURL}
	var f *pollTest
	f = newPollTest(t, &pollTestOptions{
		video:   true,
		content: fallbackContent,
		initial: postedWithoutVideo(),
		recent:  serveTimeline(t, &onX),
		post: func(w http.ResponseWriter, r *http.Request, _ feed.Item) {
			testutil.Equal(t, r.URL.Path, "/post-video")
			testutil.Equal(t, r.Header.Get("X-Post-Captions"), "")
			// The earlier post must be saved before X can receive its replacement.
			if saved := savedEntry(t, f); saved.Status != state.StatusPosting || saved.Replaces == nil || saved.Replaces.PostURL != replacedURL {
				t.Errorf("state at POST time = %#v", saved)
			}
			onX = append(onX, videoURL)
			testutil.JSON(t, w, http.StatusOK, map[string]string{"url": videoURL, "captions": "attached"})
		},
		delete: func(w http.ResponseWriter, r *http.Request) {
			var payload struct{ ID string }
			testutil.Equal(t, json.NewDecoder(r.Body).Decode(&payload), nil)
			testutil.Equal(t, payload.ID, "100")
			// Deletion only follows a confirmed and saved video post.
			if saved := savedEntry(t, f); saved.Status != state.StatusPosted || saved.PostURL != videoURL {
				t.Errorf("state at delete time = %#v", saved)
			}
			onX = slices.DeleteFunc(onX, func(url string) bool { return url == replacedURL })
			testutil.JSON(t, w, http.StatusOK, map[string]string{"deleted": payload.ID})
		},
	})

	entry := f.poll()
	if entry.Status != state.StatusPosted || entry.PostURL != videoURL || entry.Format != state.FormatVideoCaptions ||
		entry.Replaces != nil || entry.AwaitingVideo || entry.FoundOnX || entry.LastError != "" {
		t.Fatalf("replaced entry = %#v", entry)
	}
	f.assertCalls(1, 1)
	testutil.Equal(t, f.deleteCalls.Load(), 1)
	assertSaved(t, f, &entry)

	f.poll()
	f.assertCalls(1, 1)
	testutil.Equal(t, f.deleteCalls.Load(), 1)
}

func TestPollReplacementRequiresLateVideoWithinWindow(t *testing.T) {
	legacy := postedWithoutVideo()
	legacy.AwaitingVideo = false
	for _, tt := range []struct {
		name      string
		initial   state.Entry
		published time.Duration
		window    time.Duration
		noVideo   bool
	}{
		// Legacy state, video fallbacks and posts found on X never await a video.
		{name: "not awaiting video", initial: legacy, published: time.Hour, window: 6 * time.Hour},
		{name: "outside window", initial: postedWithoutVideo(), published: 6*time.Hour + time.Minute, window: 6 * time.Hour},
		{name: "disabled", initial: postedWithoutVideo(), published: time.Hour},
		{name: "no video", initial: postedWithoutVideo(), published: time.Hour, window: 6 * time.Hour, noVideo: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newPollTest(t, &pollTestOptions{
				video:     !tt.noVideo,
				published: testNow.Add(-tt.published),
				initial:   tt.initial,
			})
			f.app.cfg.VideoReplaceWindow = tt.window
			before := f.app.store.Items[f.item.GUID]
			testutil.Equal(t, f.poll(), before)
			f.assertCalls(0, 0)
			testutil.Equal(t, f.deleteCalls.Load(), 0)
		})
	}
}

func TestPollKeepsEarlierPostWhenVideoCannotBePosted(t *testing.T) {
	for _, tt := range []struct {
		name      string
		download  bool
		wantPosts int32
		wantError string
	}{
		{name: "download failure", download: true, wantError: "503"},
		{name: "upload failure", wantPosts: 1, wantError: "bad video"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			onX := []string{replacedURL}
			f := newPollTest(t, &pollTestOptions{
				video:   true,
				initial: postedWithoutVideo(),
				recent:  serveTimeline(t, &onX),
				content: func(w http.ResponseWriter, r *http.Request) {
					if tt.download && r.URL.Path == "/video.mp4" {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					fallbackContent(w, r)
				},
				post: func(w http.ResponseWriter, r *http.Request, _ feed.Item) {
					testutil.Equal(t, r.URL.Path, "/post-video")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, `{"error":"bad video","clicked":false,"stage":"video"}`)
				},
			})

			entry := f.poll()
			want := postedWithoutVideo()
			if entry.Status != want.Status || entry.PostURL != want.PostURL || entry.Format != want.Format || entry.Attempts != want.Attempts ||
				entry.Replaces != nil || entry.AwaitingVideo || !strings.Contains(entry.LastError, "video replacement abandoned") ||
				!strings.Contains(entry.LastError, tt.wantError) {
				t.Fatalf("restored entry = %#v", entry)
			}
			f.assertCalls(1, tt.wantPosts)
			assertSaved(t, f, &entry)

			// The earlier post is final; the video is not tried again.
			f.poll()
			f.assertCalls(1, tt.wantPosts)
			testutil.Equal(t, f.deleteCalls.Load(), 0)
		})
	}
}

func TestPollKeepsEarlierPostWhenVideoIsRemoved(t *testing.T) {
	initial := pendingDeletion()
	initial.Status, initial.PostURL, initial.Format = state.StatusRetry, "", ""
	f := newPollTest(t, &pollTestOptions{initial: initial, content: fallbackContent})
	entry := f.poll()
	if entry.Status != state.StatusPosted || entry.PostURL != replacedURL || entry.Format != state.FormatImage || entry.Replaces != nil ||
		!strings.Contains(entry.LastError, "the feed no longer lists a video") {
		t.Fatalf("entry = %#v", entry)
	}
	f.assertCalls(1, 0)
	testutil.Equal(t, f.deleteCalls.Load(), 0)
}

func TestPollReconcilesUncertainReplacement(t *testing.T) {
	now := testNow
	onX := []string{replacedURL}
	f := newPollTest(t, &pollTestOptions{
		now:     func() time.Time { return now },
		video:   true,
		content: fallbackContent,
		initial: postedWithoutVideo(),
		recent:  serveTimeline(t, &onX),
		post: func(_ http.ResponseWriter, _ *http.Request, _ feed.Item) {
			onX = append(onX, videoURL)
			panic(http.ErrAbortHandler)
		},
	})

	entry := f.poll()
	if entry.Status != state.StatusUncertain || entry.Replaces == nil {
		t.Fatalf("entry after lost response = %#v", entry)
	}
	testutil.Equal(t, f.deleteCalls.Load(), 0)

	f.app.store = reloadPollState(t, f)
	now = now.Add(uncertainMinimum)
	entry = f.poll()
	if entry.Status != state.StatusPosted || entry.PostURL != videoURL || !entry.FoundOnX || entry.Replaces != nil {
		t.Fatalf("reconciled entry = %#v", entry)
	}
	f.assertCalls(2, 1)
	testutil.Equal(t, f.deleteCalls.Load(), 1)
}

func TestPollRechecksXAfterFailedDeletion(t *testing.T) {
	now := testNow
	onX := []string{replacedURL, videoURL}
	f := newPollTest(t, &pollTestOptions{
		now:     func() time.Time { return now },
		initial: pendingDeletion(),
		recent:  serveTimeline(t, &onX),
		delete: func(w http.ResponseWriter, _ *http.Request) {
			// The post is gone, but the confirmation is lost.
			onX = []string{videoURL}
			testutil.JSON(t, w, http.StatusInternalServerError, map[string]string{"error": "confirmation missing"})
		},
	})

	entry := f.poll()
	if entry.Replaces == nil || entry.Replaces.DeleteAttempts != 1 || !entry.Replaces.NextDeleteAt.Equal(testNow.Add(retryInitial)) ||
		!strings.Contains(entry.LastError, "confirmation missing") {
		t.Fatalf("entry after failed deletion = %#v", entry)
	}
	testutil.Equal(t, reloadPollState(t, f).Items[f.item.GUID].Replaces.DeleteAttempts, 1)

	now = testNow.Add(retryInitial - time.Second)
	f.poll()
	f.assertCalls(1, 0)

	now = testNow.Add(retryInitial)
	entry = f.poll()
	if entry.Replaces != nil || entry.LastError != "" || entry.PostURL != videoURL {
		t.Fatalf("entry after recheck = %#v", entry)
	}
	f.assertCalls(2, 0)
	testutil.Equal(t, f.deleteCalls.Load(), 1)
}

func TestPollGivesUpOnReplacedPostDeletion(t *testing.T) {
	t.Run("attempts exhausted", func(t *testing.T) {
		now := testNow
		onX := []string{replacedURL, videoURL}
		f := newPollTest(t, &pollTestOptions{
			now:     func() time.Time { return now },
			initial: pendingDeletion(),
			recent:  serveTimeline(t, &onX),
			delete: func(w http.ResponseWriter, _ *http.Request) {
				testutil.JSON(t, w, http.StatusInternalServerError, map[string]string{"error": "menu missing"})
			},
		})
		var entry state.Entry
		for range maxDeleteAttempts {
			entry = f.poll()
			if entry.Replaces != nil {
				now = entry.Replaces.NextDeleteAt
			}
		}
		if entry.Status != state.StatusPosted || entry.PostURL != videoURL || entry.Replaces != nil ||
			!strings.Contains(entry.LastError, replacedURL+" must be deleted manually") || !strings.Contains(entry.LastError, "menu missing") {
			t.Fatalf("entry after exhausted deletion = %#v", entry)
		}
		testutil.Equal(t, f.deleteCalls.Load(), maxDeleteAttempts)
		f.poll()
		testutil.Equal(t, f.deleteCalls.Load(), maxDeleteAttempts)
	})

	t.Run("beyond MAX_AGE", func(t *testing.T) {
		f := newPollTest(t, &pollTestOptions{published: testNow.Add(-25 * time.Hour), initial: pendingDeletion()})
		entry := f.poll()
		if entry.Replaces != nil || !strings.Contains(entry.LastError, "must be deleted manually: the retry window expired") {
			t.Fatalf("expired deletion = %#v", entry)
		}
		f.assertCalls(0, 0)
		testutil.Equal(t, f.deleteCalls.Load(), 0)
	})
}

func TestPollExpiredReplacement(t *testing.T) {
	for _, tt := range []struct {
		status, wantStatus, wantPostURL, wantError string
	}{
		{status: state.StatusRetry, wantStatus: state.StatusPosted, wantPostURL: replacedURL, wantError: "video replacement abandoned: the retry window expired"},
		{status: state.StatusUncertain, wantStatus: state.StatusFailedTerminal, wantError: "the replaced post " + replacedURL + " is still on X"},
	} {
		t.Run(tt.status, func(t *testing.T) {
			initial := pendingDeletion()
			initial.Status, initial.PostURL, initial.Format, initial.Attempts = tt.status, "", state.FormatVideoCaptions, 2
			f := newPollTest(t, &pollTestOptions{video: true, published: testNow.Add(-25 * time.Hour), initial: initial})
			entry := f.poll()
			if entry.Status != tt.wantStatus || entry.PostURL != tt.wantPostURL || !strings.Contains(entry.LastError, tt.wantError) {
				t.Fatalf("expired replacement = %#v", entry)
			}
			if tt.status == state.StatusRetry && (entry.Format != state.FormatImage || entry.Attempts != 1 || entry.Replaces != nil) {
				t.Errorf("earlier post not restored: %#v", entry)
			}
			f.assertCalls(0, 0)
			testutil.Equal(t, f.deleteCalls.Load(), 0)
		})
	}
}

func TestPollUsesVideoAddedBeforeFirstPost(t *testing.T) {
	f := newPollTest(t, &pollTestOptions{
		video:   true,
		content: fallbackContent,
		initial: state.Entry{Status: state.StatusRetry, Format: state.FormatText, FallbackReason: "no og:image", AwaitingVideo: true, Attempts: 1},
		post: func(w http.ResponseWriter, r *http.Request, _ feed.Item) {
			testutil.Equal(t, r.URL.Path, "/post-video")
			testutil.JSON(t, w, http.StatusOK, map[string]string{"url": videoURL, "captions": "attached"})
		},
	})
	entry := f.poll()
	if entry.Status != state.StatusPosted || entry.Format != state.FormatVideoCaptions || entry.FallbackReason != "" || entry.AwaitingVideo {
		t.Fatalf("entry = %#v", entry)
	}
	f.assertCalls(1, 1)
	testutil.Equal(t, f.deleteCalls.Load(), 0)
}

func TestPollDryRunNeverDeletes(t *testing.T) {
	f := newPollTest(t, &pollTestOptions{initial: pendingDeletion()})
	f.app.cfg.DryRun = true
	if entry := f.poll(); entry.Replaces != nil {
		t.Errorf("dry run preview = %#v", entry)
	}
	f.assertCalls(0, 0)
	testutil.Equal(t, f.deleteCalls.Load(), 0)
}
