// Package config loads orchestrator settings from the environment.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the orchestrator settings.
type Config struct {
	FeedURL             string
	AllowedHosts        []string
	PosterURL           string
	StateFile           string
	HeartbeatURL        string
	PollInterval        time.Duration
	PostDelay           time.Duration
	MaxAge              time.Duration
	PosterNotReadyAfter time.Duration
	PollStaleAfter      time.Duration
	AlertReminder       time.Duration
	DryRun              bool
	Graph               GraphConfig
}

// GraphConfig holds optional Microsoft Graph mail settings.
type GraphConfig struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	FromAddress  string
	Recipients   []string
}

// Complete reports whether all mail settings are present; partial settings disable mail.
func (g *GraphConfig) Complete() bool {
	return g.TenantID != "" && g.ClientID != "" && g.ClientSecret != "" &&
		g.FromAddress != "" && len(g.Recipients) > 0
}

// Load reads environment settings, applies defaults and rejects invalid values.
func Load() (*Config, error) {
	cfg := &Config{
		FeedURL:      cmp.Or(os.Getenv("FEED_URL"), "https://www.zuidwestupdate.nl/feed/"),
		AllowedHosts: commaSeparated(os.Getenv("ALLOWED_HOSTS")),
		PosterURL:    cmp.Or(os.Getenv("POSTER_URL"), "http://127.0.0.1:8081"),
		StateFile:    cmp.Or(os.Getenv("STATE_FILE"), "/data/state.json"),
		HeartbeatURL: os.Getenv("HEARTBEAT_URL"),
		Graph: GraphConfig{
			TenantID:     os.Getenv("GRAPH_TENANT_ID"),
			ClientID:     os.Getenv("GRAPH_CLIENT_ID"),
			ClientSecret: os.Getenv("GRAPH_CLIENT_SECRET"),
			FromAddress:  os.Getenv("GRAPH_FROM_ADDRESS"),
			Recipients:   commaSeparated(os.Getenv("ALERT_RECIPIENTS")),
		},
	}

	var err error
	if cfg.PollInterval, err = parse("POLL_INTERVAL", 2*time.Minute, time.ParseDuration); err != nil {
		return nil, err
	}
	if cfg.PostDelay, err = parse("POST_DELAY", 30*time.Second, time.ParseDuration); err != nil {
		return nil, err
	}
	if cfg.MaxAge, err = parse("MAX_AGE", 24*time.Hour, time.ParseDuration); err != nil {
		return nil, err
	}
	if cfg.PosterNotReadyAfter, err = parse("POSTER_NOT_READY_AFTER", 30*time.Minute, time.ParseDuration); err != nil {
		return nil, err
	}
	if cfg.PollStaleAfter, err = parse("POLL_STALE_AFTER", 15*time.Minute, time.ParseDuration); err != nil {
		return nil, err
	}
	if cfg.AlertReminder, err = parse("ALERT_REMINDER_INTERVAL", 24*time.Hour, time.ParseDuration); err != nil {
		return nil, err
	}
	if cfg.DryRun, err = parse("DRY_RUN", false, strconv.ParseBool); err != nil {
		return nil, err
	}

	if cfg.PollInterval < 30*time.Second {
		return nil, fmt.Errorf("POLL_INTERVAL must be at least 30s, got %s", cfg.PollInterval)
	}
	if cfg.PostDelay < 0 {
		return nil, errors.New("POST_DELAY must not be negative")
	}
	// Zero would mark every new article as missed before it is posted.
	if cfg.MaxAge <= 0 {
		return nil, errors.New("MAX_AGE must be greater than zero")
	}
	if cfg.PosterNotReadyAfter <= 0 {
		return nil, errors.New("POSTER_NOT_READY_AFTER must be greater than zero")
	}
	// Leave room for the next scheduled poll before declaring it stale.
	if cfg.PollStaleAfter <= cfg.PollInterval {
		return nil, fmt.Errorf("POLL_STALE_AFTER must be longer than POLL_INTERVAL (%s), got %s", cfg.PollInterval, cfg.PollStaleAfter)
	}
	if cfg.AlertReminder <= 0 {
		return nil, errors.New("ALERT_REMINDER_INTERVAL must be greater than zero")
	}
	if cfg.Graph.Complete() {
		if err := validateAddress("GRAPH_FROM_ADDRESS", cfg.Graph.FromAddress); err != nil {
			return nil, err
		}
		for _, address := range cfg.Graph.Recipients {
			if err := validateAddress("ALERT_RECIPIENTS", address); err != nil {
				return nil, err
			}
		}
	}
	return cfg, nil
}

func commaSeparated(raw string) []string {
	var result []string
	for address := range strings.SplitSeq(raw, ",") {
		if address = strings.TrimSpace(address); address != "" {
			result = append(result, address)
		}
	}
	return result
}

func validateAddress(key, value string) error {
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return fmt.Errorf("%s must contain plain e-mail addresses", key)
	}
	return nil
}

// parse converts key's value, using fallback when unset or empty.
func parse[T any](key string, fallback T, conv func(string) (T, error)) (T, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	parsed, err := conv(v)
	if err != nil {
		return parsed, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}
