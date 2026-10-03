package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/config"
	"github.com/oszuidwest/zw-xposter/internal/notify"
	"github.com/oszuidwest/zw-xposter/internal/poster"
	operational "github.com/oszuidwest/zw-xposter/internal/status"
	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestOperationalAlertsFollowFailureAndRecovery(t *testing.T) {
	// Replace HTTP only; keep real alert delivery. The global override forbids t.Parallel.
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	synctest.Test(t, func(t *testing.T) {
		ready := false
		subjects := []string{}
		// In-memory HTTP keeps all requests inside the synctest bubble.
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Host {
			case "poster.invalid":
				testutil.Equal(t, r.URL.Path, "/ready")
				if !ready {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			case "login.microsoftonline.com":
				_, _ = io.WriteString(w, `{"access_token":"offline-token","expires_in":3600}`)
			case "graph.microsoft.com":
				var mail struct {
					Message struct {
						Subject string `json:"subject"`
					} `json:"message"`
				}
				if err := json.NewDecoder(r.Body).Decode(&mail); err != nil {
					t.Error(err)
				}
				subjects = append(subjects, mail.Message.Subject)
				w.WriteHeader(http.StatusAccepted)
			default:
				t.Errorf("unexpected outbound request to %s%s", r.Host, r.URL)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		http.DefaultTransport = server.Client().Transport
		cfg := &config.Config{
			PosterNotReadyAfter: 5 * time.Minute, PollStaleAfter: 10 * time.Minute,
			Graph: config.GraphConfig{TenantID: "offline", ClientID: "offline", ClientSecret: "offline",
				FromAddress: "sender@example.invalid", Recipients: []string{"ops@example.invalid"}},
		}
		a := &app{cfg: cfg, now: time.Now, startedAt: time.Now(), poster: poster.New("https://poster.invalid"),
			status: operational.New(time.Hour), alerts: notify.New(&cfg.Graph, 24*time.Hour)}
		defer a.alerts.Close()
		a.status.RecordPoll(time.Now())
		check := func(want ...string) {
			t.Helper()
			a.evaluateOperationalAlerts(t.Context())
			synctest.Wait()
			if !slices.Equal(subjects, want) {
				t.Fatalf("mail subjects = %v, want %v", subjects, want)
			}
		}
		const suffix = " - ZuidWest X Poster"
		posterAlert := "[ALERT] Poster is not ready" + suffix
		pollAlert := "[ALERT] Orchestrator polls are stale" + suffix
		posterRecovery := "[RECOVERED] Poster is ready again" + suffix
		pollRecovery := "[RECOVERED] Orchestrator polling recovered" + suffix
		check()
		time.Sleep(5*time.Minute - time.Nanosecond)
		check()
		time.Sleep(time.Nanosecond)
		check(posterAlert)
		check(posterAlert)
		time.Sleep(5 * time.Minute)
		check(posterAlert, pollAlert)
		ready = true
		check(posterAlert, pollAlert, posterRecovery)
		a.status.RecordPoll(time.Now())
		check(posterAlert, pollAlert, posterRecovery, pollRecovery)
		check(posterAlert, pollAlert, posterRecovery, pollRecovery)
	})
}
