package main

import (
	"context"
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

func fallbackContent(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/video.mp4":
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(testutil.MP4(time.Minute))
	case "/share.png":
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, "image")
	default:
		_, _ = fmt.Fprintf(w, `<meta property="og:image" content="http://%s/share.png">`, html.EscapeString(r.Host))
	}
}

func TestFallbackClassification(t *testing.T) {
	for _, tt := range []struct {
		name, body, format, status string
		posts                      int32
	}{
		{"video media", `{"error":"bad video","clicked":false,"stage":"video"}`, "image", state.StatusPosted, 2},
		{"login", `{"error":"login","clicked":false,"stage":"session"}`, "video_captions", state.StatusRetry, 1},
		{"generic", `{"error":"disabled button","clicked":false,"stage":"service"}`, "video_captions", state.StatusRetry, 1},
		{"unknown", `{"error":"video failed","clicked":false,"stage":"future"}`, "video_captions", state.StatusRetry, 1},
		{"missing stage", `{"error":"video failed","clicked":false}`, "video_captions", state.StatusRetry, 1},
		{"wrong stage", `{"error":"image failed","clicked":false,"stage":"image"}`, "video_captions", state.StatusRetry, 1},
		{"post click", `{"error":"video failed","clicked":true,"stage":"video"}`, "video_captions", state.StatusUncertain, 1},
		{"missing click", `{"error":"video failed","stage":"video"}`, "video_captions", state.StatusUncertain, 1},
		{"malformed", `{"clicked":false,"stage":"video"`, "video_captions", state.StatusUncertain, 1},
		{"missing error", `{"clicked":false,"stage":"video"}`, "video_captions", state.StatusUncertain, 1},
		{"invalid click type", `{"error":"video failed","clicked":"false","stage":"video"}`, "video_captions", state.StatusUncertain, 1},
		{"lost response", "", "video_captions", state.StatusUncertain, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newPollTest(t, &pollTestOptions{content: fallbackContent, post: func(w http.ResponseWriter, r *http.Request, _ feed.Item) {
				if r.URL.Path == "/post-video" {
					if tt.body == "" {
						connection, _, err := w.(http.Hijacker).Hijack()
						testutil.NoError(t, err)
						testutil.NoError(t, connection.Close())
						return
					}
					w.WriteHeader(500)
					_, _ = io.WriteString(w, tt.body)
					return
				}
				testutil.JSON(t, w, 200, map[string]string{"url": "https://x.invalid/status/1"})
			}})
			f.items[0].VideoURL = strings.TrimSuffix(f.item.Link, "/article") + "/video.mp4"
			entry := f.poll()
			testutil.Equal(t, entry.Format, tt.format)
			testutil.Equal(t, entry.Status, tt.status)
			f.assertCalls(1, tt.posts)
		})
	}
}

func TestFallbackPersistsAcrossRestart(t *testing.T) {
	for _, format := range []string{"video", "image", "text"} {
		t.Run(format, func(t *testing.T) {
			now := testNow
			var f *pollTest
			fail := true
			var requests []string
			f = newPollTest(t, &pollTestOptions{
				now:     func() time.Time { return now },
				content: fallbackContent,
				post: func(w http.ResponseWriter, r *http.Request, item feed.Item) {
					saved, err := state.Load(f.app.cfg.StateFile)
					testutil.NoError(t, err)
					testutil.Equal(t, saved.Items[item.GUID].Status, state.StatusPosting)
					current := "video_captions"
					if r.URL.Path == "/post-video" {
						if r.Header.Get("X-Post-Captions") == "none" {
							current = "video"
						}
					} else {
						var payload struct {
							Text  string
							Image any
						}
						testutil.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
						testutil.Equal(t, payload.Text, postText(&item))
						current = "text"
						if payload.Image != nil {
							current = "image"
						}
					}
					requests = append(requests, current)
					if fail {
						body := map[string]any{"error": "browser unavailable", "clicked": false, "stage": "service"}
						switch {
						case format == "video":
							body["captions"], body["fallbackReason"] = "none", "ElevenLabs HTTP 401"
						case current == "video_captions":
							body["stage"] = "video"
						case format == "text" && current == "image":
							body["stage"] = "image"
						}
						testutil.JSON(t, w, 500, body)
						return
					}
					testutil.Equal(t, current, format)
					testutil.JSON(t, w, 200, map[string]string{"url": "https://x.invalid/status/1", "captions": "none"})
				},
			})
			f.items[0].VideoURL = strings.TrimSuffix(f.item.Link, "/article") + "/video.mp4"
			entry := f.poll()
			testutil.Equal(t, entry.Status, state.StatusRetry)
			testutil.Equal(t, entry.Format, format)
			if entry.FallbackReason == "" || !strings.Contains(entry.LastError, "browser unavailable") {
				t.Fatalf("missing outcome details: %+v", entry)
			}
			f.app.store = reloadPollState(t, f)
			now = entry.NextAttemptAt
			fail = false
			entry = f.poll()
			testutil.Equal(t, entry.Status, state.StatusPosted)
			testutil.Equal(t, entry.Format, format)
			testutil.Equal(t, requests[len(requests)-1], format)
			testutil.Equal(t, f.recentCalls.Load(), 2)
			before := len(requests)
			f.poll()
			testutil.Equal(t, len(requests), before)
		})
	}
}

func TestCancellationDuringMediaPreparationStopsFallback(t *testing.T) {
	for _, video := range []bool{false, true} {
		t.Run(fmt.Sprint("video=", video), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			f := newPollTest(t, &pollTestOptions{content: func(w http.ResponseWriter, _ *http.Request) {
				calls++
				cancel()
				http.Error(w, "cancelled", http.StatusServiceUnavailable)
			}})
			if video {
				f.items[0].VideoURL = strings.TrimSuffix(f.item.Link, "/article") + "/video.mp4"
			}
			testutil.ErrorContains(t, f.app.poll(ctx), "context canceled")
			f.assertCalls(1, 0)
			testutil.Equal(t, calls, 1)
		})
	}
}

func TestFallbackSaveFailureStopsNextPost(t *testing.T) {
	var f *pollTest
	f = newPollTest(t, &pollTestOptions{content: fallbackContent, post: func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
		testutil.NoError(t, os.Mkdir(f.app.cfg.StateFile+".tmp", 0o700))
		testutil.JSON(t, w, 500, map[string]any{"error": "upload failed", "clicked": false, "stage": "image"})
	}})
	if err := f.app.poll(t.Context()); err == nil {
		t.Fatal("fallback continued after failed save")
	}
	f.assertCalls(1, 1)
	testutil.Equal(t, reloadPollState(t, f).Items[f.item.GUID].Status, state.StatusPosting)
}
