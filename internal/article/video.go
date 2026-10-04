package article

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"time"
)

// MaxVideoSize also bounds the streamed body in poster/media.mjs.
const MaxVideoSize = 512 << 20

// Video holds a downloaded MP4 on disk, ready to stream to the poster.
type Video struct {
	*os.File
}

// Close releases the file and removes the temporary download.
func (v *Video) Close() error {
	return errors.Join(v.File.Close(), os.Remove(v.Name()))
}

// FetchVideo downloads a bounded MP4 through the same URL and redirect policy as images.
func FetchVideo(ctx context.Context, client *http.Client, videoURL string) (_ *Video, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create video request: %w", err)
	}
	// A large video needs more time; retain the content client's transport protections.
	downloadClient := *client
	downloadClient.Timeout = 5 * time.Minute
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch video: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch video: status %s", resp.Status)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType != "video/mp4" {
		return nil, fmt.Errorf("unsupported video type %q", resp.Header.Get("Content-Type"))
	}
	if resp.ContentLength > MaxVideoSize {
		return nil, fmt.Errorf("video exceeds %d bytes", MaxVideoSize)
	}
	file, err := os.CreateTemp("", "xposter-video-*.mp4")
	if err != nil {
		return nil, fmt.Errorf("create video file: %w", err)
	}
	video := &Video{File: file}
	defer func() {
		if err != nil {
			if closeErr := video.Close(); closeErr != nil {
				slog.Warn("remove failed video download", "error", closeErr)
			}
		}
	}()
	size, err := io.Copy(file, io.LimitReader(resp.Body, MaxVideoSize+1))
	if err != nil {
		return nil, fmt.Errorf("download video: %w", err)
	}
	if size == 0 || size > MaxVideoSize {
		return nil, fmt.Errorf("video size %d is outside 1..%d bytes", size, MaxVideoSize)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind video: %w", err)
	}
	return video, nil
}
