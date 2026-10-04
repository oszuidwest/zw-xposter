package article

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/oszuidwest/zw-xposter/internal/safehttp"
	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestFetchVideo(t *testing.T) {
	// Redirect temporary downloads so cleanup can be verified, including failures.
	t.Setenv("TMPDIR", t.TempDir())
	server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4; charset=binary")
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/wrong-type":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "not a video")
		case "/large":
			w.Header().Set("Content-Length", fmt.Sprint(MaxVideoSize+1))
		case "/truncated":
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, "truncated")
		case "/empty":
		case "/redirect":
			http.Redirect(w, r, "/video", http.StatusFound)
		default:
			_, _ = io.WriteString(w, "synthetic video bytes")
		}
	})
	for _, tt := range []struct {
		name, path, wantErr string
	}{
		{name: "MP4", path: "/video"},
		{name: "redirect", path: "/redirect"},
		{name: "missing", path: "/missing", wantErr: "404"},
		{name: "wrong MIME", path: "/wrong-type", wantErr: "unsupported video type"},
		{name: "oversize header", path: "/large", wantErr: "exceeds"},
		{name: "truncated body", path: "/truncated", wantErr: "unexpected EOF"},
		{name: "empty", path: "/empty", wantErr: "video size 0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			video, err := FetchVideo(t.Context(), server.Client(), server.URL+tt.path)
			testutil.ErrorContains(t, err, tt.wantErr)
			if tt.wantErr == "" {
				body, err := io.ReadAll(video)
				testutil.NoError(t, err)
				testutil.Equal(t, string(body), "synthetic video bytes")
				testutil.NoError(t, video.Close())
			}
			files, err := os.ReadDir(os.TempDir())
			testutil.NoError(t, err)
			if len(files) != 0 {
				t.Fatalf("temporary downloads leaked: %v", files)
			}
		})
	}
}

func TestFetchVideoPreservesContentRestrictions(t *testing.T) {
	t.Parallel()
	client, err := safehttp.New("https://example.com/feed", nil)
	testutil.NoError(t, err)
	for _, target := range []string{"https://untrusted.invalid/video.mp4", "http://example.com/video.mp4"} {
		video, err := FetchVideo(t.Context(), client, target)
		if err == nil || video != nil {
			t.Fatalf("untrusted video fetched: %s", target)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = FetchVideo(ctx, client, "https://example.com/video.mp4")
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("cancelled download: %v", err)
	}
}
