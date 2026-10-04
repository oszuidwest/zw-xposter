package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/feed"
	"github.com/oszuidwest/zw-xposter/internal/state"
	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestPollVideoTakesPriority(t *testing.T) {
	for _, tt := range []struct {
		name         string
		dryRun       bool
		failDownload bool
		postError    string
		wantStatus   string
		wantPosts    int32
	}{
		{name: "video replaces image", wantStatus: state.StatusPosted, wantPosts: 1},
		{name: "dry run never posts", dryRun: true, wantStatus: state.StatusPosted},
		{name: "download failure retries without image", failDownload: true, wantStatus: state.StatusRetry},
		{name: "upload failure retries", postError: `{"error":"encoding failed","clicked":false}`, wantStatus: state.StatusRetry, wantPosts: 1},
		{name: "post-click failure reconciles", postError: `{"error":"confirmation missing","clicked":true}`, wantStatus: state.StatusUncertain, wantPosts: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var contentCalls int
			fixture := newPollTest(t, &pollTestOptions{
				content: func(w http.ResponseWriter, r *http.Request) {
					contentCalls++
					if r.URL.Path != "/video.mp4" {
						t.Errorf("video article fetched image/page: %s", r.URL.Path)
					}
					if tt.failDownload {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					w.Header().Set("Content-Type", "video/mp4")
					_, _ = io.WriteString(w, "synthetic MP4")
				},
				post: func(w http.ResponseWriter, r *http.Request, item feed.Item) {
					testutil.Equal(t, r.URL.Path, "/post-video")
					testutil.Equal(t, r.Header.Get("Content-Type"), "video/mp4")
					text, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Post-Text"))
					testutil.NoError(t, err)
					testutil.Equal(t, string(text), postText(&item))
					body, err := io.ReadAll(r.Body)
					testutil.NoError(t, err)
					testutil.Equal(t, string(body), "synthetic MP4")
					if tt.postError != "" {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, tt.postError)
						return
					}
					testutil.JSON(t, w, http.StatusOK, map[string]any{"url": "https://x.invalid/status/video"})
				},
			})
			fixture.items[0].VideoURL = strings.TrimSuffix(fixture.item.Link, "/article") + "/video.mp4"
			fixture.app.cfg.DryRun = tt.dryRun
			entry := fixture.poll()
			testutil.Equal(t, entry.Status, tt.wantStatus)
			fixture.assertCalls(1, tt.wantPosts)
			testutil.Equal(t, contentCalls, 1)
			if tt.wantStatus == state.StatusRetry || tt.wantStatus == state.StatusUncertain {
				testutil.Equal(t, entry.Attempts, 1)
				if entry.LastError == "" || !entry.NextAttemptAt.After(testNow) {
					t.Fatalf("failure was not scheduled for retry: %#v", entry)
				}
			}
		})
	}
}

func TestPollRecoversPersistedPosting(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{initial: state.Entry{Status: state.StatusPosting, Attempts: 1}})
	testutil.NoError(t, fixture.app.store.Save())
	fixture.app.store = reloadPollState(t, fixture)
	fixture.poll()
	entry := reloadPollState(t, fixture).Items[fixture.item.GUID]
	if entry.Status != state.StatusUncertain || entry.Attempts != 1 || !entry.NextAttemptAt.Equal(testNow.Add(5*time.Minute)) {
		t.Fatalf("recovered entry = %#v", entry)
	}
	fixture.assertCalls(0, 0)
}

func TestPollWaitsUntilRetryIsEligible(t *testing.T) {
	// Both are scheduled two minutes out; an uncertain outcome waits at least five.
	for _, tt := range []struct {
		status   string
		eligible time.Duration
	}{
		{state.StatusRetry, 2 * time.Minute},
		{state.StatusUncertain, 5 * time.Minute},
	} {
		t.Run(tt.status, func(t *testing.T) {
			now := testNow
			fixture := newPollTest(t, &pollTestOptions{
				now: func() time.Time { return now },
				initial: state.Entry{
					Status: tt.status, Attempts: 1, UpdatedAt: testNow,
					NextAttemptAt: testNow.Add(2 * time.Minute),
				},
			})
			eligible := testNow.Add(tt.eligible)
			now = eligible.Add(-time.Nanosecond)
			fixture.poll()
			fixture.assertCalls(0, 0)
			now = eligible
			fixture.poll()
			entry := reloadPollState(t, fixture).Items[fixture.item.GUID]
			if fixture.postCalls.Load() != 1 || fixture.recentCalls.Load() != 1 || entry.Status != state.StatusPosted {
				t.Fatalf("eligible retry: entry=%#v, recent=%d, posts=%d", entry, fixture.recentCalls.Load(), fixture.postCalls.Load())
			}
			now = eligible.Add(time.Minute)
			fixture.poll()
			fixture.assertCalls(1, 1)
		})
	}
}

func TestPollDeduplicatesDifferentGUIDsForSameArticle(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{
		extra: []feed.Item{{GUID: "guid-2", Title: "Same article", Link: "/article", Published: testNow}},
	})
	fixture.poll()
	saved := reloadPollState(t, fixture)
	first, second := saved.Items["guid-1"], saved.Items["guid-2"]
	if fixture.postCalls.Load() != 1 || first.Status != state.StatusPosted || second.Status != state.StatusPosted ||
		!second.FoundOnX || first.PostURL != second.PostURL {
		t.Fatalf("posts=%d, first=%#v, second=%#v", fixture.postCalls.Load(), first, second)
	}
}

func TestPollReconcilesAfterOutcomeSaveFails(t *testing.T) {
	now := testNow
	var fixture *pollTest
	fixture = newPollTest(t, &pollTestOptions{
		now: func() time.Time { return now },
		recent: func(w http.ResponseWriter, _ *http.Request, item feed.Item) {
			posts := []any{}
			if fixture.postCalls.Load() > 0 {
				posts = append(posts, map[string]any{"url": "https://x.invalid/status/1", "urls": []string{item.Link}})
			}
			testutil.JSON(t, w, http.StatusOK, map[string]any{"complete": true, "posts": posts})
		},
		post: func(w http.ResponseWriter, _ *http.Request, item feed.Item) {
			// The write-ahead entry must be saved before X receives the request.
			saved, err := state.Load(fixture.app.cfg.StateFile)
			if err != nil {
				t.Error(err)
				http.Error(w, "read state", http.StatusInternalServerError)
				return
			}
			entry := saved.Items[item.GUID]
			if entry.Status != state.StatusPosting || entry.Attempts != 1 {
				t.Errorf("state at POST time = %#v", entry)
			}
			if err := os.Mkdir(fixture.app.cfg.StateFile+".tmp", 0o700); err != nil {
				t.Error(err)
				http.Error(w, "block save", http.StatusInternalServerError)
				return
			}
			testutil.JSON(t, w, http.StatusOK, map[string]any{"url": "https://x.invalid/status/1"})
		},
	})
	if err := fixture.app.poll(t.Context()); err == nil {
		t.Fatal("poll succeeded despite failed outcome save")
	}
	saved := reloadPollState(t, fixture)
	onDisk, inMemory := saved.Items[fixture.item.GUID].Status, fixture.app.store.Items[fixture.item.GUID].Status
	if onDisk != state.StatusPosting || inMemory != state.StatusPosting {
		t.Fatalf("after failed outcome save: disk=%q, memory=%q; want write-ahead %q in both", onDisk, inMemory, state.StatusPosting)
	}
	testutil.NoError(t, os.Remove(fixture.app.cfg.StateFile+".tmp"))
	fixture.app.store = saved // restart from disk, not the previous in-memory outcome
	fixture.poll()
	if entry := reloadPollState(t, fixture).Items[fixture.item.GUID]; entry.Status != state.StatusUncertain {
		t.Fatalf("restart did not recover: %#v", entry)
	}
	now = now.Add(5 * time.Minute)
	fixture.poll()
	entry := reloadPollState(t, fixture).Items[fixture.item.GUID]
	if fixture.postCalls.Load() != 1 || entry.Status != state.StatusPosted || !entry.FoundOnX {
		t.Fatalf("reconciliation: posts=%d, entry=%#v", fixture.postCalls.Load(), entry)
	}
}

func TestPollSendsArticleTextAndImage(t *testing.T) {
	const imageData = "synthetic image bytes"
	fixture := newPollTest(t, &pollTestOptions{
		content: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/share.png" {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write([]byte(imageData))
				return
			}
			_, _ = fmt.Fprintf(w, `<meta property="og:image" content="http://%s/share.png">`, html.EscapeString(r.Host))
		},
		post: func(w http.ResponseWriter, r *http.Request, item feed.Item) {
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("unexpected post request: %s, %s", r.Method, r.Header.Get("Content-Type"))
			}
			var payload struct {
				Text  string `json:"text"`
				Image struct {
					Name string `json:"name"`
					MIME string `json:"mime"`
					Data string `json:"data"`
				} `json:"image"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			data, err := base64.StdEncoding.DecodeString(payload.Image.Data)
			if err != nil || string(data) != imageData || payload.Image.Name != "share.png" || payload.Image.MIME != "image/png" {
				t.Errorf("image = %#v, decode error = %v", payload.Image, err)
			}
			if payload.Text != "Synthetic article "+item.Link {
				t.Errorf("text = %q", payload.Text)
			}
			testutil.JSON(t, w, http.StatusOK, map[string]any{"url": "https://x.invalid/status/1"})
		},
	})
	fixture.poll()
	if posts, url := fixture.postCalls.Load(), reloadPollState(t, fixture).Items[fixture.item.GUID].PostURL; posts != 1 || url != "https://x.invalid/status/1" {
		t.Fatalf("posts=%d, saved post URL=%q", posts, url)
	}
}

func TestPollUnconfirmedSuccessBecomesUncertain(t *testing.T) {
	fixture := newPollTest(t, &pollTestOptions{
		post: func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
			testutil.JSON(t, w, http.StatusOK, map[string]any{})
		},
	})
	fixture.poll()
	entry := reloadPollState(t, fixture).Items[fixture.item.GUID]
	if entry.Status != state.StatusUncertain || !strings.Contains(entry.LastError, "without a post URL") {
		t.Fatalf("unconfirmed success = %#v", entry)
	}
}
