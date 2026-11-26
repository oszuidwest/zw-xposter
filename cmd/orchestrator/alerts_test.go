package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/config"
	"github.com/oszuidwest/zw-xposter/internal/notify"
	"github.com/oszuidwest/zw-xposter/internal/poster"
	"github.com/oszuidwest/zw-xposter/internal/state"
	operational "github.com/oszuidwest/zw-xposter/internal/status"
	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func graphMailHandler(t *testing.T, receive func(subject, body string)) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "login.microsoftonline.com":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"offline-token","expires_in":3600}`)
		case "graph.microsoft.com":
			var mail struct {
				Message struct {
					Subject string
					Body    struct{ Content string }
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&mail); err != nil {
				t.Error(err)
			}
			receive(mail.Message.Subject, mail.Message.Body.Content)
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected outbound request to %s%s", r.Host, r.URL)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
}

func TestOperationalAlertsCoverOneOutageWithItsCause(t *testing.T) {
	// Replace HTTP only; keep real alert delivery. The global override forbids t.Parallel.
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	synctest.Test(t, func(t *testing.T) {
		ready, feedUp := false, true
		var mails []string
		graph := graphMailHandler(t, func(subject, body string) { mails = append(mails, subject+"\n"+body) })
		// In-memory HTTP keeps all requests inside the synctest bubble.
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Host {
			case "poster.invalid":
				testutil.Equal(t, r.URL.Path, "/ready")
				if !ready {
					http.Error(w, `{"loggedIn":false}`, http.StatusServiceUnavailable)
					return
				}
			case "feed.invalid":
				if !feedUp {
					http.Error(w, "feed down", http.StatusBadGateway)
					return
				}
				writeFeed(t, w, nil)
			default:
				graph(w, r)
			}
		}))
		http.DefaultTransport = server.Client().Transport
		store, err := state.Load(filepath.Join(t.TempDir(), "state.json"))
		testutil.NoError(t, err)
		cfg := &config.Config{
			FeedURL: "https://feed.invalid/feed", PosterNotReadyAfter: 5 * time.Minute, PollStaleAfter: 10 * time.Minute,
			Graph: config.GraphConfig{TenantID: "offline", ClientID: "offline", ClientSecret: "offline",
				FromAddress: "sender@example.invalid", Recipients: []string{"ops@example.invalid"}},
		}
		a := &app{cfg: cfg, now: time.Now, startedAt: time.Now(), store: store, http: server.Client(),
			poster: poster.New("https://poster.invalid"), status: operational.New(time.Hour),
			alerts: notify.New(&cfg.Graph, 24*time.Hour)}
		defer a.alerts.Close()
		poll := func(wantErr string) {
			t.Helper()
			if err := a.pollAndReport(t.Context()); wantErr == "" {
				testutil.NoError(t, err)
			} else {
				testutil.ErrorContains(t, err, wantErr)
			}
		}
		check := func(want ...[]string) {
			t.Helper()
			a.evaluateOperationalAlerts(t.Context())
			synctest.Wait()
			for i, lines := range want {
				if i >= len(mails) {
					t.Fatalf("mails = %q, want %d", mails, len(want))
				}
				for _, line := range lines {
					if !strings.Contains(mails[i], line) {
						t.Errorf("mail %d = %q, want %q", i, mails[i], line)
					}
				}
			}
			if len(mails) != len(want) {
				t.Fatalf("mails = %q, want %d", mails, len(want))
			}
			mails = nil
		}
		const recovered = "[RECOVERED] Poster and polling recovered"

		// The poster fails first; stale polls join the same alert.
		poll("")
		check()
		time.Sleep(5*time.Minute - time.Nanosecond)
		check()
		time.Sleep(time.Nanosecond)
		check([]string{"[ALERT] Poster is not ready", `Error: poster readiness returned 503 Service Unavailable: {"loggedIn":false}`})
		time.Sleep(5 * time.Minute)
		check()
		ready = true
		check()
		poll("")
		check([]string{recovered})

		// Polls fail first and the mail says why. A poster that is not ready, even for less than
		// its threshold, keeps the alert open after polls recover.
		feedUp = false
		poll("502 Bad Gateway")
		time.Sleep(10 * time.Minute)
		check([]string{"[ALERT] Orchestrator polls are stale", "Last error: fetch feed", "502 Bad Gateway"})
		ready = false
		time.Sleep(5 * time.Minute)
		check()
		feedUp = true
		poll("")
		check()
		ready = true
		check([]string{recovered})
	})
}

func TestPollAlertsOnceAboutAnUnfinishedArticleAndItsCompletion(t *testing.T) {
	now := testNow
	fixture := newPollTest(t, &pollTestOptions{
		now: func() time.Time { return now },
		initial: state.Entry{Status: state.StatusRetry, Attempts: 3, LastError: "composer selector broken",
			NextAttemptAt: testNow.Add(10 * time.Minute)},
	})
	fixture.app.startedAt = testNow.Add(-time.Hour + time.Minute)
	assertMails(t, pollMails(t, fixture))
	now = testNow.Add(time.Minute)
	assertMails(t, pollMails(t, fixture), []string{"Article is not complete on X after an hour", "Last error: composer selector broken"})
	assertMails(t, pollMails(t, fixture))
	now = testNow.Add(10 * time.Minute)
	assertMails(t, pollMails(t, fixture), []string{"Article is now complete on X", "Post: " + mainURL})
}
