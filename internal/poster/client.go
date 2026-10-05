// Package poster talks to the Playwright poster service.
package poster

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/article"
)

// MaxLookbackHours is the most X history /recent reads; it must match
// MAX_RECENT_HOURS in poster/server.mjs.
const MaxLookbackHours = 336

// MaxLookback is MaxLookbackHours as a duration.
const MaxLookback = MaxLookbackHours * time.Hour

// LookbackHours rounds d up to the whole hours /recent accepts.
func LookbackHours(d time.Duration) int {
	return int(math.Ceil(d.Hours()))
}

// maxResponseBytes bounds a poster response body.
const maxResponseBytes = 1 << 20

// Poster contract values shared with poster/server.mjs.
const (
	StageVideo   = "video"
	StageImage   = "image"
	CaptionsNone = "none"
)

// Client calls the poster HTTP API.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for the poster at baseURL.
func New(baseURL string) *Client {
	// Logging in and uploading media can take a while; the poster gives up after
	// POST_TIMEOUT_MS so its error response arrives first.
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 5 * time.Minute}}
}

type request struct {
	Text  string         `json:"text"`
	Image *article.Image `json:"image,omitempty"`
}

type response struct {
	Result
	Error   string `json:"error"`
	Clicked *bool  `json:"clicked"`
	Stage   string `json:"stage"`
}

// Result describes the confirmed post and any caption downgrade within the request.
type Result struct {
	URL            string `json:"url"`
	Captions       string `json:"captions,omitempty"`
	FallbackReason string `json:"fallbackReason,omitempty"`
}

// PostError is a poster failure. Clicked is true unless a pre-click failure is confirmed.
type PostError struct {
	Status  string
	Message string
	Clicked bool
	Stage   string
}

// Error preserves the poster's message for operational logs.
func (e *PostError) Error() string {
	return fmt.Sprintf("poster returned %s: %s", e.Status, e.Message)
}

// Post publishes text with an optional image and returns the post URL.
func (c *Client) Post(ctx context.Context, text string, img *article.Image) (string, error) {
	body, err := json.Marshal(request{Text: text, Image: img})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/post", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	result, err := c.sendPost(req, c.http)
	return result.URL, err
}

// PostVideo streams an MP4 instead of buffering a base64 copy in JSON.
// With a *PostError, Result still reports any caption downgrade.
func (c *Client) PostVideo(ctx context.Context, text string, video io.Reader, captions bool) (Result, error) {
	// NopCloser stops the transport from closing a caller-owned *article.Video.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/post-video", io.NopCloser(video))
	if err != nil {
		return Result{}, fmt.Errorf("create video post: %w", err)
	}
	req.Header.Set("Content-Type", "video/mp4")
	req.Header.Set("X-Post-Text", base64.StdEncoding.EncodeToString([]byte(text)))
	if !captions {
		req.Header.Set("X-Post-Captions", CaptionsNone)
	}
	client := *c.http
	// Receipt has 5 minutes; the poster bounds queueing and all browser work to
	// VIDEO_POST_TIMEOUT_MS (24).
	client.Timeout = 30 * time.Minute
	return c.sendPost(req, &client)
}

func (c *Client) sendPost(req *http.Request, client *http.Client) (Result, error) {
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, err
	}
	if len(raw) > maxResponseBytes {
		return Result{}, errors.New("poster response exceeds 1 MiB")
	}
	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("poster returned %s: %s", resp.Status, raw)
	}
	if resp.StatusCode != http.StatusOK {
		if strings.TrimSpace(out.Error) == "" {
			return Result{}, fmt.Errorf("poster returned %s without an error description", resp.Status)
		}
		return Result{Captions: out.Captions, FallbackReason: out.FallbackReason}, &PostError{
			Status:  resp.Status,
			Message: out.Error,
			Clicked: out.Clicked == nil || *out.Clicked,
			Stage:   out.Stage,
		}
	}
	if strings.TrimSpace(out.URL) == "" {
		return Result{}, errors.New("poster returned success without a post URL")
	}
	return out.Result, nil
}

// Post is a published post on the account.
type Post struct {
	ID   string   `json:"id"`
	URL  string   `json:"url"`
	Text string   `json:"text"`
	URLs []string `json:"urls"`
}

// Recent returns own posts from lookback rounded up to whole hours, newest first.
// Incomplete timelines return an error because they cannot rule out duplicates.
func (c *Client) Recent(ctx context.Context, lookback time.Duration) ([]Post, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/recent?hours=%d", c.baseURL, LookbackHours(lookback)), http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Posts    []Post `json:"posts"`
		Complete bool   `json:"complete"`
		Error    string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("poster returned %s: %w", resp.Status, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poster returned %s: %s", resp.Status, out.Error)
	}
	if !out.Complete {
		return nil, errors.New("poster returned an incomplete recent-post timeline")
	}
	return out.Posts, nil
}

// Ready checks cached session health without browser work.
// A non-200 response includes the cached state in the error.
func (c *Client) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/ready", http.NoBody)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("read poster readiness: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("poster readiness returned %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return nil
}
