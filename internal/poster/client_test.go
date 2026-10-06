package poster

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
		lookback  time.Duration
		hours     string
		wantErr   bool
		wantPosts int
	}{
		{name: "complete timeline", fixture: "recent-complete.json", lookback: 24*time.Hour + time.Minute, hours: "25", wantPosts: 2},
		{name: "incomplete timeline", fixture: "recent-incomplete.json", lookback: 24*time.Hour + time.Minute, hours: "25", wantErr: true},
		// Pins the Go side of the limit; server.test.mjs pins the poster side.
		{name: "maximum lookback", lookback: MaxLookback, hours: "336"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(`{"posts":[],"complete":true}`)
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
					t.Fatal("Recent() error = nil, want an incomplete-timeline error")
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

func TestStatusID(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		url, want string
		ok        bool
	}{
		{url: "https://x.com/zwupdate/status/900000000000000101", want: "900000000000000101", ok: true},
		{url: "https://x.com/zwupdate/status/900000000000000101/", want: "900000000000000101", ok: true},
		{url: "https://x.com/zwupdate/status/900000000000000101/analytics"},
		{url: "https://x.com/zwupdate/status/abc"},
		{url: "https://x.com/zwupdate"},
		{url: "https://x.com/status/"},
		{url: "%"},
	} {
		id, ok := StatusID(tt.url)
		if id != tt.want || ok != tt.ok {
			t.Errorf("StatusID(%q) = %q, %v; want %q, %v", tt.url, id, ok, tt.want, tt.ok)
		}
	}
}

func TestClientDelete(t *testing.T) {
	t.Parallel()

	const postURL = "https://x.com/fixture_account/status/900000000000000101"
	tests := []struct {
		name    string
		url     string
		status  int
		body    string
		wantErr string
	}{
		{name: "confirmed", url: postURL, status: http.StatusOK, body: `{"deleted":"900000000000000101"}`},
		{name: "poster failure", url: postURL, status: http.StatusInternalServerError, body: `{"error":"menu missing"}`, wantErr: "menu missing"},
		{name: "other post confirmed", url: postURL, status: http.StatusOK, body: `{"deleted":"900000000000000102"}`, wantErr: "instead of"},
		{name: "unconfirmed success", url: postURL, status: http.StatusOK, body: `{}`, wantErr: "instead of"},
		{name: "malformed", url: postURL, status: http.StatusOK, body: `{`, wantErr: "poster returned"},
		{name: "not a post URL", url: "https://x.com/fixture_account", wantErr: "not an X post URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var calls int
			server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				testutil.Equal(t, r.Method, http.MethodPost)
				testutil.Equal(t, r.URL.Path, "/delete")
				var payload struct{ ID string }
				testutil.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				testutil.Equal(t, payload.ID, "900000000000000101")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})

			err := New(server.URL).Delete(t.Context(), tt.url)
			if tt.wantErr == "" {
				testutil.NoError(t, err)
			} else {
				testutil.ErrorContains(t, err, tt.wantErr)
			}
			if tt.status == 0 && calls != 0 {
				t.Errorf("invalid URL reached the poster")
			}
		})
	}
}
