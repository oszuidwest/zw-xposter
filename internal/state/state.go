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
	PostURL           string    `json:"post_url,omitempty"`   // set once confirmed, also while its link reply is pending
	ReplyURL          string    `json:"reply_url,omitempty"`  // the reply that carries the article link
	FoundOnX          bool      `json:"found_on_x,omitempty"` // includes our own earlier attempts
	Attempts          int       `json:"attempts,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
	Format            string    `json:"format,omitempty"`       // a Format constant; empty in legacy state
	LinkInPost        bool      `json:"link_in_post,omitempty"` // the last attempt carried the article link in its post
	FallbackReason    string    `json:"fallback_reason,omitempty"`
	AwaitingVideo     bool      `json:"awaiting_video,omitempty"` // media chosen while the feed listed no video
	PublishedAt       time.Time `json:"published_at,omitzero"`
	NextAttemptAt     time.Time `json:"next_attempt_at,omitzero"`
	ReplayRequestedAt time.Time `json:"replay_requested_at,omitzero"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Store holds entries keyed by feed GUID.
type Store struct {
	path   string
	exists bool
	Items  map[string]Entry `json:"items"`
}

// Load rejects invalid state; a missing file yields an empty store.
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path) //nolint:gosec // The state path is operator-configured, not user input.
	if errors.Is(err, os.ErrNotExist) {
		s.Items = map[string]Entry{}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.Items == nil {
		return nil, fmt.Errorf("state file %q has no items object", path)
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
// It syncs the file and its directory, so a saved write-ahead record survives a host crash.
func (s *Store) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // The state path is operator-configured, not user input.
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return closeAfter(f, err)
	}
	if err := closeAfter(f, f.Sync()); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	d, err := os.Open(dir) //nolint:gosec // The state path is operator-configured, not user input.
	if err != nil {
		return err
	}
	return closeAfter(d, d.Sync())
}

// closeAfter closes f and returns err, or the close error if err is nil.
func closeAfter(f *os.File, err error) error {
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
