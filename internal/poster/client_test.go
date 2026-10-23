package poster

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestClientRecent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fixture   string
		body      string
		lookback  time.Duration
		hours     string
		wantErr   bool
		wantPosts int
	}{
		{name: "complete timeline", fixture: "recent-complete.json", lookback: 24*time.Hour + time.Minute, hours: "25", wantPosts: 2},
		{name: "incomplete timeline", fixture: "recent-incomplete.json", lookback: 24*time.Hour + time.Minute, hours: "25", wantErr: true},
		{name: "missing posts", body: `{"complete":true}`, hours: "0", wantErr: true},
		{name: "null posts", body: `{"posts":null,"complete":true}`, hours: "0", wantErr: true},
		{name: "maximum lookback", lookback: MaxLookback, hours: strconv.Itoa(MaxLookbackHours)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(`{"posts":[],"complete":true}`)
			if tt.body != "" {
				body = []byte(tt.body)
			}
			if tt.fixture != "" {
				body = readFixture(t, tt.fixture)
			}
			server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
				testutil.Equal(t, r.URL.Path, "/recent")
				testutil.Equal(t, r.URL.Query().Get("hours"), tt.hours)
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write(body); err != nil {
					t.Errorf("write response: %v", err)
				}
			})

			posts, err := New(server.URL).Recent(t.Context(), tt.lookback)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Recent() error = nil, want a timeline error")
				}
				if posts != nil {
					t.Errorf("Recent() posts = %#v, want nil", posts)
				}
				return
			}
			testutil.NoError(t, err)
			if len(posts) != tt.wantPosts || (len(posts) > 0 && posts[0].ID != "900000000000000101") {
				t.Errorf("Recent() posts = %#v, want %d fixture posts", posts, tt.wantPosts)
			}
		})
	}
}

func TestClientPostError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fixture     string
		body        string
		video       bool
		wantClicked bool
		wantStage   string
		wantResult  Result
	}{
		{name: "before click", fixture: "post-error-before-click.json", wantStage: "service"},
		{name: "after click", fixture: "post-error-after-click.json", wantClicked: true, wantStage: "service"},
		{
			name: "video stage", fixture: "post-error-video-stage.json", video: true, wantStage: StageVideo,
			wantResult: Result{Captions: CaptionsNone, FallbackReason: "caption generation: ElevenLabs transcription returned HTTP 503"},
		},
		{name: "missing click state", body: `{"error":"outcome unknown"}`, wantClicked: true},
		{name: "null click state", body: `{"error":"outcome unknown","clicked":null}`, wantClicked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(tt.body)
			if tt.fixture != "" {
				body = readFixture(t, tt.fixture)
			}
			server := testutil.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				if _, err := w.Write(body); err != nil {
					t.Errorf("write response: %v", err)
				}
			})

			var result Result
			var err error
			if tt.video {
				result, err = New(server.URL).PostVideo(t.Context(), "synthetic post", strings.NewReader("mp4"), true)
			} else {
				_, err = New(server.URL).Post(t.Context(), "synthetic post", nil)
			}
			postErr, ok := errors.AsType[*PostError](err)
			if !ok {
				t.Fatalf("post error = %T %v, want *PostError", err, err)
			}
			testutil.Equal(t, postErr.Clicked, tt.wantClicked)
			testutil.Equal(t, postErr.Status, "500 Internal Server Error")
			testutil.Equal(t, postErr.Stage, tt.wantStage)
			testutil.Equal(t, result, tt.wantResult)
		})
	}
}

func TestClientReady(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		wantErr string
	}{
		{name: "ready", status: http.StatusOK},
		{name: "not ready", status: http.StatusServiceUnavailable, wantErr: `503 Service Unavailable: {"loggedIn":false,"loginBlockedUntil":"2026-10-03T12:30:00Z"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
				testutil.Equal(t, r.URL.Path, "/ready")
				testutil.JSON(t, w, tt.status, map[string]any{
					"loggedIn":          tt.status == http.StatusOK,
					"loginBlockedUntil": "2026-10-03T12:30:00Z",
				})
			})

			err := New(server.URL).Ready(t.Context())
			testutil.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contract", name)) //nolint:gosec // Test fixture name is fixed by the caller.
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestClientPostRequiresConfirmation(t *testing.T) {
	for _, body := range []string{`{}`, `{"url":""}`, `{"url":"  "}`, `null`} {
		t.Run(body, func(t *testing.T) {
			server := testutil.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			})
			url, err := New(server.URL).Post(t.Context(), "Article", nil)
			if err == nil || url != "" {
				t.Fatalf("Post() = %q, %v; want unconfirmed outcome error", url, err)
			}
			if _, ok := errors.AsType[*PostError](err); ok {
				t.Fatal("missing confirmation must not be classified as a safe pre-click error")
			}
		})
	}
}

func TestClientReply(t *testing.T) {
	t.Parallel()

	const (
		parent = "https://x.com/fixture_account/status/900000000000000601"
		reply  = "https://x.com/fixture_account/status/900000000000000602"
		text   = "👉 Lees het hele verhaal: https://example.invalid/article"
	)
	server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
		testutil.Equal(t, r.URL.Path, "/reply")
		var body map[string]string
		testutil.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		testutil.Equal(t, body["inReplyTo"], parent)
		testutil.Equal(t, body["text"], text)
		testutil.JSON(t, w, http.StatusOK, map[string]any{"url": reply})
	})

	url, err := New(server.URL).Reply(t.Context(), parent, text)
	testutil.NoError(t, err)
	testutil.Equal(t, url, reply)
}

func TestClientReplyError(t *testing.T) {
	t.Parallel()

	server := testutil.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		testutil.JSON(t, w, http.StatusInternalServerError, map[string]any{"error": "reply failed", "clicked": false, "stage": "service"})
	})
	_, err := New(server.URL).Reply(t.Context(), "https://x.com/fixture_account/status/1", "text")
	postErr, ok := errors.AsType[*PostError](err)
	if !ok || postErr.Clicked {
		t.Fatalf("Reply() error = %v, want a pre-click *PostError", err)
	}
}
