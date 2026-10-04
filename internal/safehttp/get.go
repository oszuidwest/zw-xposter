package safehttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Open returns the response for url once its status is 200; the caller closes
// the body. A non-200 response is an error.
func Open(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	return resp, nil
}

// Get returns the body of url, at most limit bytes, and its content type. A
// larger or non-200 response is an error.
func Get(ctx context.Context, client *http.Client, url string, limit int64) (body []byte, contentType string, err error) {
	resp, err := Open(ctx, client, url)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > limit {
		return nil, "", fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, strings.TrimSpace(resp.Header.Get("Content-Type")), nil
}
