// Package notify sends deduplicated operational alerts through Microsoft Graph.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/config"
)

const sendTimeout = 90 * time.Second

// Event describes an alert message or operational condition.
type Event struct {
	Key     string
	Summary string
	Details string
}

type mailer interface {
	SendMail(context.Context, []string, string, string) error
}

type message struct {
	subject string
	body    string
}

// Service queues alerts and rate-limits reminders per condition.
// A nil Service discards events; callers must recheck active conditions.
type Service struct {
	mu         sync.Mutex
	reminder   time.Duration
	recipients []string
	mailer     mailer
	lastSent   map[string]time.Time // last enqueue time per active condition, not delivery time
	jobs       chan message
	closed     bool
	work       sync.WaitGroup
}

// New starts mail delivery, or returns nil for incomplete Graph settings.
func New(cfg *config.GraphConfig, reminder time.Duration) *Service {
	if !cfg.Complete() {
		return nil
	}
	return newService(newGraphClient(cfg), cfg.Recipients, reminder)
}

func newService(m mailer, recipients []string, reminder time.Duration) *Service {
	service := &Service{
		reminder:   reminder,
		recipients: slices.Clone(recipients),
		mailer:     m,
		lastSent:   make(map[string]time.Time),
		jobs:       make(chan message, 128),
	}
	service.work.Go(service.deliver)
	return service
}

// IsConfigured reports whether Microsoft Graph delivery is enabled.
func (s *Service) IsConfigured() bool {
	return s != nil
}

// Alert queues a new condition or a reminder after the reminder interval.
func (s *Service) Alert(event Event) {
	if !s.IsConfigured() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	kind := "ALERT"
	if lastSent, active := s.lastSent[event.Key]; active {
		if now.Sub(lastSent) < s.reminder {
			return
		}
		kind = "REMINDER"
	}
	if s.enqueueLocked(formatMessage(kind, event, now)) {
		s.lastSent[event.Key] = now
	}
}

// Notify queues a one-time alert without creating condition state.
func (s *Service) Notify(event Event) {
	if !s.IsConfigured() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueueLocked(formatMessage("ALERT", event, time.Now()))
}

// Resolve clears an active condition once its recovery message is queued.
func (s *Service) Resolve(event Event) {
	if !s.IsConfigured() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, active := s.lastSent[event.Key]; active && s.enqueueLocked(formatMessage("RECOVERED", event, time.Now())) {
		delete(s.lastSent, event.Key)
	}
}

// Close waits for queued delivery attempts; failures are logged.
func (s *Service) Close() {
	if !s.IsConfigured() {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.jobs)
	}
	s.mu.Unlock()
	s.work.Wait()
}

func (s *Service) enqueueLocked(mail message) bool {
	if s.closed {
		return false
	}
	select {
	case s.jobs <- mail:
		return true
	default:
		slog.Error("alert email queue is full", "subject", mail.subject)
		return false
	}
}

func (s *Service) deliver() {
	for job := range s.jobs {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		err := s.mailer.SendMail(ctx, s.recipients, job.subject, job.body)
		cancel()
		if err != nil {
			slog.Error("alert email failed", "subject", job.subject, "error", err)
			continue
		}
		slog.Info("alert email sent", "subject", job.subject)
	}
}

func formatMessage(kind string, event Event, at time.Time) message {
	body := fmt.Sprintf("%s\n\nTimestamp: %s\nCondition: %s", event.Summary, at.UTC().Format(time.RFC3339), event.Key)
	if event.Details != "" {
		body += "\n\n" + event.Details
	}
	return message{
		subject: fmt.Sprintf("[%s] %s - ZuidWest X Poster", kind, event.Summary),
		body:    body,
	}
}
