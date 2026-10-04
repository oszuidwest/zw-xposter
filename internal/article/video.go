package article

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/safehttp"
)

// maxVideoSize also bounds the streamed body in poster/media.mjs.
const maxVideoSize = 512 << 20

// ErrUnsupportedVideo marks a video that no retry can post: not a readable MP4,
// over the size limit, or outside X's duration limits.
var ErrUnsupportedVideo = errors.New("unsupported video")

// Video holds a downloaded MP4 on disk, ready to stream to the poster.
type Video struct {
	*os.File
}

// Close releases the file and removes the temporary download.
func (v *Video) Close() error {
	return errors.Join(v.File.Close(), os.Remove(v.Name()))
}

// FetchVideo downloads a bounded MP4 through the same URL and redirect policy as images.
// Videos that X cannot accept return an error wrapping ErrUnsupportedVideo.
func FetchVideo(ctx context.Context, client *http.Client, videoURL string) (*Video, error) {
	return fetchVideo(ctx, client, videoURL, maxVideoSize)
}

func fetchVideo(ctx context.Context, client *http.Client, videoURL string, limit int64) (_ *Video, err error) {
	// A large video needs more time; retain the content client's transport protections.
	downloadClient := *client
	downloadClient.Timeout = 5 * time.Minute
	resp, err := safehttp.Open(ctx, &downloadClient, videoURL)
	if err != nil {
		return nil, fmt.Errorf("fetch video: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType != "video/mp4" {
		return nil, fmt.Errorf("%w type %q; only video/mp4 is supported", ErrUnsupportedVideo, resp.Header.Get("Content-Type"))
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrUnsupportedVideo, limit)
	}
	file, err := os.CreateTemp("", "xposter-video-*.mp4")
	if err != nil {
		return nil, fmt.Errorf("create video file: %w", err)
	}
	video := &Video{File: file}
	defer func() {
		if err != nil {
			err = errors.Join(err, video.Close())
		}
	}()
	size, err := io.Copy(file, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download video: %w", err)
	}
	if size > limit {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrUnsupportedVideo, limit)
	}
	if size == 0 {
		return nil, errors.New("video is empty")
	}
	duration, err := mp4Duration(file)
	if err != nil {
		return nil, fmt.Errorf("read video duration: %w", err)
	}
	// An unrecorded duration is left to X's own check.
	if duration != 0 && (duration < minVideoDuration || duration > maxVideoDuration) {
		return nil, fmt.Errorf("%w: duration %s is outside %s..%s", ErrUnsupportedVideo, duration.Round(time.Millisecond), minVideoDuration, maxVideoDuration)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind video: %w", err)
	}
	return video, nil
}
