// Package state records which feed items have been handled.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Status of a feed item.
const (
	StatusSeeded         = "seeded" // present on first run, intentionally not posted
	StatusPosting        = "posting"
	StatusPosted         = "posted"
	StatusRetry          = "retry"
	StatusUncertain      = "uncertain"
	StatusMissed         = "missed"
	StatusFailedTerminal = "failed_terminal"
)

// Publication format of a feed item, from richest to plainest.
const (
	FormatVideoCaptions = "video_captions"
	FormatVideo         = "video"
	FormatImage         = "image"
	FormatText          = "text"
)

// Entry is the stored outcome for one feed item.
type Entry struct {
	Title             string    `json:"title"`
	Link              string    `json:"link"`
	Status            string    `json:"status"`
	PostURL           string    `json:"post_url,omitempty"`
	FoundOnX          bool      `json:"found_on_x,omitempty"` // found on X, possibly our own earlier attempt
	Attempts          int       `json:"attempts,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
	Format            string    `json:"format,omitempty"` // a Format constant; empty in legacy state
	FallbackReason    string    `json:"fallback_reason,omitempty"`
	PublishedAt       time.Time `json:"published_at,omitzero"`
	NextAttemptAt     time.Time `json:"next_attempt_at,omitzero"`
	ReplayRequestedAt time.Time `json:"replay_requested_at,omitzero"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Store holds entries keyed by feed GUID; Save persists them as JSON.
type Store struct {
	path   string
	exists bool
	Items  map[string]Entry `json:"items"`
}

// Load reads the store from path. A missing file yields an empty store.
func Load(path string) (*Store, error) {
	s := &Store{path: path, Items: map[string]Entry{}}
	data, err := os.ReadFile(path) //nolint:gosec // The state path is operator-configured, not user input.
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.Items == nil {
		s.Items = map[string]Entry{}
	}
	s.exists = true
	for guid := range s.Items {
		if format := s.Items[guid].Format; !validFormat(format) {
			return nil, fmt.Errorf("state entry %q has unknown format %q", guid, format)
		}
		if status := s.Items[guid].Status; !validStatus(status) {
			return nil, fmt.Errorf("state entry %q has unknown status %q", guid, status)
		}
	}
	return s, nil
}

func validStatus(status string) bool {
	switch status {
	case StatusSeeded, StatusPosting, StatusPosted, StatusRetry, StatusUncertain, StatusMissed, StatusFailedTerminal:
		return true
	default:
		return false
	}
}

func validFormat(format string) bool {
	switch format {
	case "", FormatVideoCaptions, FormatVideo, FormatImage, FormatText:
		return true
	default:
		return false
	}
}

// Exists reports whether the state file was present when it was loaded.
func (s *Store) Exists() bool {
	return s.exists
}

// Done reports whether normal polling should skip the item.
// Posting entries must first be recovered as uncertain by the orchestrator.
func (s *Store) Done(guid string) bool {
	e, ok := s.Items[guid]
	return ok && e.Status != StatusRetry && e.Status != StatusUncertain
}

// SetAt updates e's timestamp and stores a copy; it does not write to disk.
func (s *Store) SetAt(guid string, e *Entry, now time.Time) {
	e.UpdatedAt = now.UTC()
	s.Items[guid] = *e
}

// Save atomically replaces the state file via rename; callers must serialize writes.
func (s *Store) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
