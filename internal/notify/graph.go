package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/config"
)

const (
	graphBaseURL      = "https://graph.microsoft.com/v1.0"
	graphScope        = "https://graph.microsoft.com/.default"
	tokenURLTemplate  = "https://login.microsoftonline.com/%s/oauth2/v2.0/token" //nolint:gosec // This is a public endpoint template, not a credential.
	graphHTTPTimeout  = 30 * time.Second
	graphMaxAttempts  = 4
	graphMaxRetryWait = 30 * time.Second
)

// graphClient sends plain-text mail with OAuth2 client credentials.
// Service.deliver owns it; concurrent calls would race on the token cache.
type graphClient struct {
	clientID     string
	clientSecret string
	fromAddress  string
	tokenURL     string
	baseURL      string
	httpClient   *http.Client

	token       string
	tokenExpiry time.Time
}

// newGraphClient configures Graph delivery; authentication waits until the first send.
func newGraphClient(cfg *config.GraphConfig) *graphClient {
	return &graphClient{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		fromAddress:  cfg.FromAddress,
		tokenURL:     fmt.Sprintf(tokenURLTemplate, url.PathEscape(cfg.TenantID)),
		baseURL:      graphBaseURL,
		httpClient:   &http.Client{Timeout: graphHTTPTimeout},
	}
}

// SendMail sends one message and retries transient Graph failures.
func (c *graphClient) SendMail(ctx context.Context, recipients []string, subject, body string) error {
	to := make([]map[string]map[string]string, 0, len(recipients))
	for _, address := range recipients {
		to = append(to, map[string]map[string]string{"emailAddress": {"address": address}})
	}
	payload, err := json.Marshal(map[string]any{"message": map[string]any{
		"subject":      subject,
		"body":         map[string]string{"contentType": "Text", "content": body},
		"toRecipients": to,
	}})
	if err != nil {
		return fmt.Errorf("marshal Graph mail: %w", err)
	}

	var lastErr error
	var retryWait time.Duration
	for attempt := range graphMaxAttempts {
		if retryWait > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("wait to retry Graph mail: %w", ctx.Err())
			case <-time.After(retryWait):
			}
		}
		retry, retryAfter, err := c.sendAttempt(ctx, payload)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry {
			return err
		}
		retryWait = retryAfter
		if retryWait <= 0 {
			retryWait = min(time.Second<<attempt, graphMaxRetryWait)
		}
	}
	return fmt.Errorf("microsoft Graph mail retries exhausted: %w", lastErr)
}

func (c *graphClient) sendAttempt(ctx context.Context, payload []byte) (retry bool, retryAfter time.Duration, err error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return true, 0, err
	}
	endpoint := fmt.Sprintf("%s/users/%s/sendMail", c.baseURL, url.PathEscape(c.fromAddress))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, 0, fmt.Errorf("create Graph mail request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return true, 0, fmt.Errorf("send Graph mail: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The status decides the outcome; the body only adds detail to errors.
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusOK, http.StatusNoContent:
		return false, 0, nil
	case http.StatusUnauthorized:
		c.token = ""
		return true, 0, errors.New("microsoft Graph mail authorization expired")
	case http.StatusTooManyRequests:
		return true, retryAfterDelay(resp.Header.Get("Retry-After")), fmt.Errorf("microsoft Graph mail rate limited: %s", responseBody)
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true, 0, fmt.Errorf("microsoft Graph mail returned %d: %s", resp.StatusCode, responseBody)
	default:
		return false, 0, fmt.Errorf("microsoft Graph mail returned %d: %s", resp.StatusCode, responseBody)
	}
}

func (c *graphClient) accessToken(ctx context.Context) (string, error) {
	if c.token != "" && time.Until(c.tokenExpiry) > time.Minute {
		return c.token, nil
	}

	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"grant_type":    {"client_credentials"},
		"scope":         {graphScope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create Graph token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request Graph token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("read Graph token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("microsoft Graph token endpoint returned %s: %s", resp.Status, body)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return "", fmt.Errorf("decode Graph token response: %w", err)
	}
	if token.AccessToken == "" {
		return "", errors.New("microsoft Graph token response has no access token")
	}
	c.token = token.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return c.token, nil
}

func retryAfterDelay(value string) time.Duration {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	// Cap seconds before conversion to avoid overflowing time.Duration.
	return time.Duration(min(seconds, int64(graphMaxRetryWait/time.Second))) * time.Second
}
