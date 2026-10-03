package notify

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/config"
)

type recordingMailer struct {
	messages []message
}

// The service has one writer; tests read only after synctest.Wait or Close.
func (m *recordingMailer) SendMail(_ context.Context, _ []string, subject, body string) error {
	m.messages = append(m.messages, message{subject: subject, body: body})
	return nil
}

func (m *recordingMailer) subjects() []string {
	result := make([]string, 0, len(m.messages))
	for _, message := range m.messages {
		result = append(result, message.subject)
	}
	return result
}

// The tests run in a synctest bubble: time.Sleep advances the fake clock and
// synctest.Wait returns once the delivery goroutine is idle.
func TestServiceSendsTransitionsAndReminder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, mailer := newTestService()
		event := Event{Key: "poster:not-ready", Summary: "Poster is not ready", Details: "logged out"}

		service.Alert(event)
		service.Alert(event)
		synctest.Wait()
		if got := mailer.subjects(); len(got) != 1 || got[0] != "[ALERT] Poster is not ready - ZuidWest X Poster" {
			t.Fatalf("subjects after transition = %v", got)
		}

		time.Sleep(23 * time.Hour)
		service.Alert(event)
		synctest.Wait()
		if got := len(mailer.subjects()); got != 1 {
			t.Fatalf("message count before reminder = %d, want 1", got)
		}

		time.Sleep(time.Hour)
		event.Details = "session still unavailable after rechecking"
		service.Alert(event)
		service.Alert(event)
		service.Resolve(Event{Key: event.Key, Summary: "Poster is ready again"})
		service.Resolve(Event{Key: event.Key, Summary: "Poster is ready again"})
		service.Close()
		if got := mailer.subjects(); len(got) != 3 ||
			got[1] != "[REMINDER] Poster is not ready - ZuidWest X Poster" ||
			got[2] != "[RECOVERED] Poster is ready again - ZuidWest X Poster" {
			t.Fatalf("final subjects = %v", got)
		}
		if body := mailer.messages[1].body; !strings.Contains(body, event.Details) {
			t.Errorf("reminder body = %q, want current details %q", body, event.Details)
		}
	})
}

func TestServiceNotifyDoesNotCreateConditionState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, mailer := newTestService()
		service.Notify(Event{Key: "workflow:missed:guid-1", Summary: "Article was missed"})

		time.Sleep(24 * time.Hour)
		service.Resolve(Event{Key: "workflow:missed:guid-1", Summary: "Article recovered"})
		service.Close()
		if got := mailer.subjects(); len(got) != 1 || got[0] != "[ALERT] Article was missed - ZuidWest X Poster" {
			t.Fatalf("one-time notification subjects = %v", got)
		}
	})
}

func TestServiceDisabledWithPartialConfiguration(t *testing.T) {
	service := New(&config.GraphConfig{TenantID: "tenant"}, time.Hour)
	if service.IsConfigured() {
		t.Fatal("partial Graph configuration enabled service")
	}
	service.Alert(Event{Key: "condition", Summary: "Condition"})
	service.Notify(Event{Key: "workflow:event", Summary: "Workflow event"})
	service.Close()
}

func newTestService() (*Service, *recordingMailer) {
	mailer := &recordingMailer{}
	return newService(mailer, []string{"ops@example.com"}, 24*time.Hour), mailer
}
