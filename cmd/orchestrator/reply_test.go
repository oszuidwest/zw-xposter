package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/feed"
	"github.com/oszuidwest/zw-xposter/internal/state"
	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

const replyLead = "👉 Lees het hele verhaal:"

// newReplyTest serves an article image and enables the link reply.
func newReplyTest(t *testing.T, opts *pollTestOptions) *pollTest {
	t.Helper()
	if opts.content == nil {
		opts.content = fallbackContent
	}
	fixture := newPollTest(t, opts)
	fixture.app.cfg.LinkInReply = true
	fixture.app.cfg.ReplyText = replyLead
	return fixture
}

func decodePayload(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var payload map[string]any
	testutil.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
	return payload
}

func ownPosts(t *testing.T, posts ...map[string]any) func(http.ResponseWriter, *http.Request, feed.Item) {
	return func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
		testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": posts, "complete": true})
	}
}

func TestPollLinkInReplyPostsHeadlineThenLink(t *testing.T) {
	var postText, replyText, inReplyTo string
	fixture := newReplyTest(t, &pollTestOptions{
		post: func(w http.ResponseWriter, r *http.Request, _ feed.Item) {
			payload := decodePayload(t, r)
			postText, _ = payload["text"].(string)
			if payload["image"] == nil {
				t.Error("the headline post must keep the article image")
			}
			testutil.JSON(t, w, http.StatusOK, map[string]any{"url": mainURL})
		},
		reply: func(w http.ResponseWriter, r *http.Request, _ feed.Item) {
			payload := decodePayload(t, r)
			replyText, _ = payload["text"].(string)
			inReplyTo, _ = payload["inReplyTo"].(string)
			testutil.JSON(t, w, http.StatusOK, map[string]any{"url": replyURL})
		},
	})

	fixture.poll()
	entry := reloadPollState(t, fixture).Items[fixture.item.GUID]
	testutil.Equal(t, postText, "Synthetic article")
	testutil.Equal(t, replyText, replyLead+" "+fixture.item.Link)
	testutil.Equal(t, inReplyTo, mainURL)
	if entry.Status != state.StatusPosted || entry.PostURL != mainURL || entry.ReplyURL != replyURL || entry.Format != state.FormatImage {
		t.Fatalf("entry = %#v", entry)
	}
	testutil.Equal(t, fixture.replyCalls.Load(), 1)
}

func TestPollLinkInReplyKeepsLinkInTextPosts(t *testing.T) {
	for _, tt := range []struct {
		name        string
		content     http.HandlerFunc
		linkInReply bool
	}{
		// An article page without og:image falls back to text.
		{"text fallback", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html></html>")) }, true},
		{"disabled", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var text string
			fixture := newReplyTest(t, &pollTestOptions{content: tt.content, post: func(w http.ResponseWriter, r *http.Request, _ feed.Item) {
				text, _ = decodePayload(t, r)["text"].(string)
				testutil.JSON(t, w, http.StatusOK, map[string]any{"url": mainURL})
			}})
			fixture.app.cfg.LinkInReply = tt.linkInReply
			entry := fixture.poll()
			testutil.Equal(t, text, "Synthetic article "+fixture.item.Link)
			testutil.Equal(t, fixture.replyCalls.Load(), 0)
			if entry.Status != state.StatusPosted || entry.ReplyURL != "" {
				t.Fatalf("entry = %#v", entry)
			}
		})
	}
}

func TestPollReplyFailureRetriesOnlyTheReply(t *testing.T) {
	var clock atomic.Int64
	clock.Store(testNow.UnixNano())
	var fail atomic.Bool
	fail.Store(true)
	fixture := newReplyTest(t, &pollTestOptions{
		now: func() time.Time { return time.Unix(0, clock.Load()).UTC() },
		recent: ownPosts(t, map[string]any{
			"id": "1", "url": mainURL, "text": "Synthetic article https://t.co/media", "urls": []string{},
		}),
		reply: func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
			if fail.Load() {
				testutil.JSON(t, w, http.StatusInternalServerError, map[string]any{"error": "reply button missing", "clicked": false})
				return
			}
			testutil.JSON(t, w, http.StatusOK, map[string]any{"url": replyURL})
		},
	})

	entry := fixture.poll()
	if entry.Status != state.StatusRetry || entry.PostURL != mainURL || !strings.Contains(entry.LastError, "link reply") {
		t.Fatalf("after failed reply: %#v", entry)
	}
	testutil.Equal(t, entry.NextAttemptAt, testNow.Add(retryDelay(2)))

	fail.Store(false)
	clock.Store(entry.NextAttemptAt.UnixNano())
	entry = fixture.poll()
	if entry.Status != state.StatusPosted || entry.ReplyURL != replyURL || entry.PostURL != mainURL {
		t.Fatalf("after retry: %#v", entry)
	}
	fixture.assertCalls(2, 1)
	testutil.Equal(t, fixture.replyCalls.Load(), 2)
}

func TestPollUncertainReplyIsReconciledWithoutSecondReply(t *testing.T) {
	var clock atomic.Int64
	clock.Store(testNow.UnixNano())
	var replied atomic.Bool
	fixture := newReplyTest(t, &pollTestOptions{
		now: func() time.Time { return time.Unix(0, clock.Load()).UTC() },
		recent: func(w http.ResponseWriter, _ *http.Request, item feed.Item) {
			posts := []any{map[string]any{"id": "1", "url": mainURL, "text": "Synthetic article", "urls": []string{}}}
			if replied.Load() {
				posts = append(posts, map[string]any{
					"id": "2", "url": replyURL, "text": replyLead + " https://t.co/link", "urls": []string{item.Link}, "replyTo": mainURL,
				})
			}
			testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": posts, "complete": true})
		},
		reply: func(_ http.ResponseWriter, _ *http.Request, _ feed.Item) {
			replied.Store(true)
			panic(http.ErrAbortHandler)
		},
	})

	entry := fixture.poll()
	if entry.Status != state.StatusUncertain || entry.PostURL != mainURL {
		t.Fatalf("after lost reply response: %#v", entry)
	}
	clock.Store(testNow.Add(uncertainMinimum).UnixNano())
	entry = fixture.poll()
	if entry.Status != state.StatusPosted || entry.ReplyURL != replyURL || entry.PostURL != mainURL || !entry.FoundOnX {
		t.Fatalf("after reconciliation: %#v", entry)
	}
	testutil.Equal(t, fixture.replyCalls.Load(), 1)
	testutil.Equal(t, fixture.postCalls.Load(), 1)
}

func TestPollUncertainPostFoundByHeadlineOnlyAddsReply(t *testing.T) {
	var inReplyTo string
	fixture := newReplyTest(t, &pollTestOptions{
		initial: state.Entry{Status: state.StatusUncertain, Attempts: 1, Format: state.FormatImage, UpdatedAt: testNow.Add(-time.Hour)},
		recent: ownPosts(t,
			map[string]any{"id": "1", "url": mainURL, "text": "Synthetic article https://t.co/media", "urls": []string{}},
			// A headline that already has its link reply belongs to another article.
			map[string]any{"id": "3", "url": "https://x.invalid/fixture/status/3", "text": "Synthetic article", "urls": []string{}},
			map[string]any{"id": "4", "url": "https://x.invalid/fixture/status/4", "text": "Lees", "urls": []string{"https://example.invalid/other"}, "replyTo": "https://x.invalid/fixture/status/3"},
		),
		reply: func(w http.ResponseWriter, r *http.Request, _ feed.Item) {
			inReplyTo, _ = decodePayload(t, r)["inReplyTo"].(string)
			testutil.JSON(t, w, http.StatusOK, map[string]any{"url": replyURL})
		},
	})

	entry := fixture.poll()
	if entry.Status != state.StatusPosted || entry.PostURL != mainURL || entry.ReplyURL != replyURL || !entry.FoundOnX {
		t.Fatalf("entry = %#v", entry)
	}
	testutil.Equal(t, inReplyTo, mainURL)
	fixture.assertCalls(1, 0)
}

func TestPollNewArticleIgnoresMatchingHeadline(t *testing.T) {
	fixture := newReplyTest(t, &pollTestOptions{
		recent: ownPosts(t, map[string]any{"id": "9", "url": "https://x.invalid/fixture/status/9", "text": "Synthetic article", "urls": []string{}}),
	})
	entry := fixture.poll()
	if entry.Status != state.StatusPosted || entry.PostURL != mainURL || entry.ReplyURL != replyURL {
		t.Fatalf("entry = %#v", entry)
	}
	fixture.assertCalls(1, 1)
}

func TestPollRecordsExistingLinkReply(t *testing.T) {
	fixture := newReplyTest(t, &pollTestOptions{
		recent: func(w http.ResponseWriter, _ *http.Request, item feed.Item) {
			testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": []any{map[string]any{
				"id": "2", "url": replyURL, "text": replyLead, "urls": []string{item.Link}, "replyTo": mainURL,
			}}, "complete": true})
		},
	})
	entry := fixture.poll()
	if entry.Status != state.StatusPosted || entry.PostURL != mainURL || entry.ReplyURL != replyURL || !entry.FoundOnX {
		t.Fatalf("entry = %#v", entry)
	}
	fixture.assertCalls(1, 0)
}

func TestPollExpiredReplyAsksForManualLink(t *testing.T) {
	fixture := newReplyTest(t, &pollTestOptions{
		published: testNow.Add(-25 * time.Hour),
		initial:   state.Entry{Status: state.StatusRetry, Attempts: 3, PostURL: mainURL, LastError: "link reply: synthetic"},
	})
	entry := fixture.poll()
	if entry.Status != state.StatusFailedTerminal || !strings.Contains(entry.LastError, "add the article link manually") {
		t.Fatalf("entry = %#v", entry)
	}
	event, ok := workflowAlert(fixture.item.GUID, &state.Entry{Status: state.StatusRetry}, &entry)
	if !ok || !strings.Contains(event.Details, "Post: "+mainURL) {
		t.Fatalf("alert = %#v", event)
	}
	fixture.assertCalls(0, 0)
}

func TestPollRecoversInterruptedReplyAsUncertain(t *testing.T) {
	fixture := newReplyTest(t, &pollTestOptions{
		initial: state.Entry{Status: state.StatusPosting, Attempts: 2, Format: state.FormatImage, PostURL: mainURL},
	})
	entry := fixture.poll()
	if entry.Status != state.StatusUncertain || entry.PostURL != mainURL {
		t.Fatalf("entry = %#v", entry)
	}
	fixture.assertCalls(0, 0)
}

func TestPollDryRunPredictsReplyWithoutPosting(t *testing.T) {
	for name, initial := range map[string]state.Entry{
		"new article":   {},
		"reply pending": {Status: state.StatusRetry, Attempts: 1, Format: state.FormatImage, PostURL: mainURL},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newReplyTest(t, &pollTestOptions{initial: initial})
			fixture.app.cfg.DryRun = true
			entry := fixture.poll()
			testutil.Equal(t, entry.Status, state.StatusPosted)
			testutil.Equal(t, fixture.postCalls.Load()+fixture.replyCalls.Load(), 0)
		})
	}
}
