package config

import (
	"testing"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestLoadOperationalDefaults(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "")
	t.Setenv("POSTER_URL", "")
	t.Setenv("POLL_INTERVAL", "")
	t.Setenv("POSTER_NOT_READY_AFTER", "")
	t.Setenv("POLL_STALE_AFTER", "")
	t.Setenv("ALERT_REMINDER_INTERVAL", "")
	t.Setenv("GRAPH_TENANT_ID", "")
	t.Setenv("GRAPH_CLIENT_ID", "")
	t.Setenv("GRAPH_CLIENT_SECRET", "")
	t.Setenv("GRAPH_FROM_ADDRESS", "")
	t.Setenv("ALERT_RECIPIENTS", "")

	cfg, err := Load()
	testutil.NoError(t, err)
	testutil.Equal(t, cfg.PosterURL, "http://127.0.0.1:8081")
	if cfg.PosterNotReadyAfter != 30*time.Minute || cfg.PollStaleAfter != 15*time.Minute {
		t.Errorf("alert thresholds = (%s, %s)", cfg.PosterNotReadyAfter, cfg.PollStaleAfter)
	}
	if cfg.AlertReminder != 24*time.Hour {
		t.Errorf("AlertReminder = %s, want 24h", cfg.AlertReminder)
	}
	if cfg.Graph.Complete() {
		t.Error("Graph.Complete() = true without configuration")
	}
}

func TestLoadAllowedHosts(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "media.zuidwestupdate.nl, cdn.example.nl")

	cfg, err := Load()
	testutil.NoError(t, err)
	if len(cfg.AllowedHosts) != 2 || cfg.AllowedHosts[1] != "cdn.example.nl" {
		t.Errorf("AllowedHosts = %v", cfg.AllowedHosts)
	}
}

func TestLoadGraphConfiguration(t *testing.T) {
	t.Setenv("GRAPH_TENANT_ID", "tenant")
	t.Setenv("GRAPH_CLIENT_ID", "client")
	t.Setenv("GRAPH_CLIENT_SECRET", "secret")
	t.Setenv("GRAPH_FROM_ADDRESS", "sender@example.com")
	t.Setenv("ALERT_RECIPIENTS", "one@example.com, two@example.com")

	cfg, err := Load()
	testutil.NoError(t, err)
	if !cfg.Graph.Complete() {
		t.Fatal("Graph.Complete() = false, want true")
	}
	if len(cfg.Graph.Recipients) != 2 || cfg.Graph.Recipients[1] != "two@example.com" {
		t.Errorf("Recipients = %v", cfg.Graph.Recipients)
	}
}

func TestLoadPartialGraphConfigurationDisablesAlerting(t *testing.T) {
	t.Setenv("GRAPH_TENANT_ID", "tenant")
	t.Setenv("GRAPH_CLIENT_ID", "")
	t.Setenv("GRAPH_CLIENT_SECRET", "")
	t.Setenv("GRAPH_FROM_ADDRESS", "")
	t.Setenv("ALERT_RECIPIENTS", "")

	cfg, err := Load()
	testutil.NoError(t, err)
	if cfg.Graph.Complete() {
		t.Fatal("partial Graph configuration unexpectedly enabled alerting")
	}
}

func TestLoadRejectsInvalidDurations(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{name: "negative post delay", env: map[string]string{"POST_DELAY": "-1s"}, wantErr: "POST_DELAY must not be negative"},
		{name: "zero post delay", env: map[string]string{"POST_DELAY": "0s"}},
		{name: "zero max age", env: map[string]string{"MAX_AGE": "0s"}, wantErr: "MAX_AGE must be greater than zero"},
		{name: "negative max age", env: map[string]string{"MAX_AGE": "-1h"}, wantErr: "MAX_AGE must be greater than zero"},
		{name: "stale threshold equals interval", env: map[string]string{"POLL_INTERVAL": "15m", "POLL_STALE_AFTER": "15m"}, wantErr: "POLL_STALE_AFTER must be longer than POLL_INTERVAL"},
		{name: "default stale threshold below interval", env: map[string]string{"POLL_INTERVAL": "20m"}, wantErr: "POLL_STALE_AFTER must be longer than POLL_INTERVAL"},
		{name: "zero stale threshold", env: map[string]string{"POLL_STALE_AFTER": "0s"}, wantErr: "POLL_STALE_AFTER must be longer than POLL_INTERVAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{"MAX_AGE", "POST_DELAY", "POLL_INTERVAL", "POLL_STALE_AFTER"} {
				t.Setenv(key, tt.env[key])
			}
			_, err := Load()
			testutil.ErrorContains(t, err, tt.wantErr)
		})
	}
}
