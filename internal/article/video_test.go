package article

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/safehttp"
	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestFetchVideo(t *testing.T) {
	// The poster enforces the same limit as MAX_VIDEO_BYTES in poster/media.mjs.
	testutil.Equal(t, maxVideoSize, 512<<20)
	// Redirect temporary downloads so cleanup can be verified, including failures.
	t.Setenv("TMPDIR", t.TempDir())
	mp4 := testutil.MP4(time.Minute)
	longest := testutil.MP4(maxVideoDuration)
	server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4; charset=binary")
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/wrong-type":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "not a video")
		case "/large":
			w.Header().Set("Content-Length", fmt.Sprint(maxVideoSize+1))
		case "/truncated":
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, "truncated")
		case "/empty":
		case "/unsized":
			// Flushing first omits Content-Length, so only the download can detect an oversize video.
			testutil.Equal(t, http.NewResponseController(w).Flush(), nil)
			_, _ = w.Write(mp4)
		case "/not-mp4":
			_, _ = io.WriteString(w, "<html>not a video</html>")
		case "/too-long":
			_, _ = w.Write(testutil.MP4(maxVideoDuration + time.Second))
		case "/too-short":
			_, _ = w.Write(testutil.MP4(minVideoDuration - time.Millisecond))
		case "/longest":
			_, _ = w.Write(longest)
		case "/redirect":
			http.Redirect(w, r, "/video", http.StatusFound)
		default:
			_, _ = w.Write(mp4)
		}
	})
	bodySize := int64(len(mp4))
	for _, tt := range []struct {
		name, path, wantErr string
		limit               int64
		permanent           bool
		want                []byte
	}{
		{name: "MP4", path: "/video", want: mp4},
		{name: "redirect", path: "/redirect", want: mp4},
		{name: "MP4 at the limit", path: "/unsized", limit: bodySize, want: mp4},
		{name: "longest allowed duration", path: "/longest", want: longest},
		{name: "missing", path: "/missing", wantErr: "404"},
		{name: "wrong MIME", path: "/wrong-type", wantErr: "unsupported video type", permanent: true},
		{name: "oversize header", path: "/large", wantErr: "exceeds", permanent: true},
		{name: "oversize body", path: "/unsized", limit: bodySize - 1, wantErr: "exceeds", permanent: true},
		{name: "truncated body", path: "/truncated", wantErr: "unexpected EOF"},
		{name: "empty", path: "/empty", wantErr: "video is empty"},
		{name: "MP4 type without MP4 structure", path: "/not-mp4", wantErr: "does not fit", permanent: true},
		{name: "longer than X allows", path: "/too-long", wantErr: "duration 20m1s is outside", permanent: true},
		{name: "shorter than X allows", path: "/too-short", wantErr: "duration 499ms is outside", permanent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			limit := cmp.Or(tt.limit, maxVideoSize)
			video, err := fetchVideo(t.Context(), server.Client(), server.URL+tt.path, limit)
			testutil.ErrorContains(t, err, tt.wantErr)
			testutil.Equal(t, errors.Is(err, ErrUnsupportedVideo), tt.permanent)
			if tt.wantErr == "" {
				body, err := io.ReadAll(video)
				testutil.NoError(t, err)
				testutil.Equal(t, string(body), string(tt.want))
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
	testutil.ErrorContains(t, err, "context canceled")
	testutil.Equal(t, errors.Is(err, ErrUnsupportedVideo), false)
}
