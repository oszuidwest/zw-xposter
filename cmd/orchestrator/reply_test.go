package main

import (
	"cmp"
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

func TestPollUncertainPostWithoutArticleLinkWaitsForReconciliation(t *testing.T) {
	fixture := newReplyTest(t, &pollTestOptions{
		initial: state.Entry{Status: state.StatusUncertain, Attempts: 1, Format: state.FormatImage, UpdatedAt: testNow.Add(-time.Hour)},
		recent: ownPosts(t,
			map[string]any{"id": "1", "url": mainURL, "text": "Synthetic article https://t.co/media", "urls": []string{}},
			map[string]any{"id": "3", "url": "https://x.invalid/fixture/status/3", "text": "Synthetic article", "urls": []string{}},
		),
	})

	entry := fixture.poll()
	if entry.Status != state.StatusUncertain || entry.PostURL != "" || entry.ReplyURL != "" {
		t.Fatalf("entry = %#v", entry)
	}
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

func TestPollOtherGUIDWaitsWhileOwnerCanStillReply(t *testing.T) {
	other := feed.Item{GUID: "guid-2", Title: "Same article", Link: "/article", Published: testNow}
	for _, tt := range []struct {
		name           string
		initial        state.Entry
		posts, replies int32
	}{
		// The first entry's reply fails in the same poll, so X shows no link yet.
		{"same poll", state.Entry{}, 1, 1},
		// The first entry waits for its reply retry when the second appears.
		{"later poll", state.Entry{Status: state.StatusRetry, Attempts: 1, Format: state.FormatImage, PostURL: mainURL, NextAttemptAt: testNow.Add(time.Hour)}, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newReplyTest(t, &pollTestOptions{
				initial: tt.initial,
				extra:   []feed.Item{other},
				reply: func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
					testutil.JSON(t, w, http.StatusInternalServerError, map[string]any{"error": "reply button missing", "clicked": false})
				},
			})
			fixture.poll()
			// Waiting saves nothing, so the stored state is read in memory.
			saved := fixture.app.store
			if first := saved.Items["guid-1"]; first.Status != state.StatusRetry || first.PostURL != mainURL || first.ReplyURL != "" {
				t.Fatalf("first = %#v", first)
			}
			if second, ok := saved.Items["guid-2"]; ok {
				t.Fatalf("second must stay untouched while the first can reply: %#v", second)
			}
			testutil.Equal(t, fixture.postCalls.Load(), tt.posts)
			testutil.Equal(t, fixture.replyCalls.Load(), tt.replies)
		})
	}
}

func TestPollOtherGUIDWaitsOnlyForUnfinishedOwner(t *testing.T) {
	for _, tt := range []struct {
		name  string
		owner state.Entry
		wait  bool
	}{
		// An unlisted owner still expires with an alert, so waiting cannot last forever.
		{"unlisted owner", state.Entry{Status: state.StatusRetry, Attempts: 1, PostURL: mainURL}, true},
		{"unlisted uncertain owner", state.Entry{Status: state.StatusUncertain, Attempts: 1, PostURL: mainURL}, true},
		// The owner gave up with an alert; the new entry publishes the article itself.
		{"expired owner", state.Entry{Status: state.StatusFailedTerminal, Attempts: 3, PostURL: mainURL}, false},
		{"other article", state.Entry{Status: state.StatusRetry, Attempts: 1, PostURL: mainURL, Link: "https://example.invalid/other"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newReplyTest(t, &pollTestOptions{})
			owner := tt.owner
			owner.Title, owner.Link, owner.PublishedAt = "Same article", cmp.Or(owner.Link, fixture.item.Link), testNow
			fixture.app.store.SetAt("guid-0", &owner, testNow.Add(-time.Hour))
			entry := fixture.poll()
			if tt.wait {
				if entry.Status != "" {
					t.Fatalf("entry must wait: %#v", entry)
				}
				fixture.assertCalls(1, 0)
				testutil.Equal(t, fixture.replyCalls.Load(), 0)
				return
			}
			if entry.Status != state.StatusPosted || entry.PostURL != mainURL || entry.ReplyURL != replyURL || entry.FoundOnX {
				t.Fatalf("entry must publish normally: %#v", entry)
			}
			fixture.assertCalls(1, 1)
			testutil.Equal(t, fixture.replyCalls.Load(), 1)
		})
	}
}

func TestPollPendingReplyIgnoresLinkUnderAnotherPost(t *testing.T) {
	var inReplyTo string
	fixture := newReplyTest(t, &pollTestOptions{
		initial: state.Entry{Status: state.StatusRetry, Attempts: 1, Format: state.FormatImage, PostURL: mainURL},
		recent: func(w http.ResponseWriter, _ *http.Request, item feed.Item) {
			testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": []any{map[string]any{
				"id": "9", "url": "https://x.invalid/fixture/status/9", "text": replyLead, "urls": []string{item.Link}, "replyTo": "https://x.invalid/fixture/status/8",
			}}, "complete": true})
		},
		reply: func(w http.ResponseWriter, r *http.Request, _ feed.Item) {
			inReplyTo, _ = decodePayload(t, r)["inReplyTo"].(string)
			testutil.JSON(t, w, http.StatusOK, map[string]any{"url": replyURL})
		},
	})
	entry := fixture.poll()
	if entry.Status != state.StatusPosted || entry.PostURL != mainURL || entry.ReplyURL != replyURL || entry.FoundOnX {
		t.Fatalf("entry = %#v", entry)
	}
	testutil.Equal(t, inReplyTo, mainURL)
	fixture.assertCalls(1, 0)
}

func TestPollReplayWaitsForExpiringOwnerThenPublishes(t *testing.T) {
	fixture := newReplyTest(t, &pollTestOptions{
		published: testNow.Add(-72 * time.Hour),
		initial:   state.Entry{Status: state.StatusRetry, Attempts: 1, Format: state.FormatImage},
		// The owner is listed but past MAX_AGE; the older replayed entry is processed first.
		extra: []feed.Item{{GUID: "guid-2", Title: "Same article", Link: "/article", Published: testNow.Add(-25 * time.Hour)}},
	})
	testutil.NoError(t, fixture.app.replay(t.Context(), fixture.item.GUID))
	owner := state.Entry{
		Title: "Same article", Link: fixture.item.Link, Status: state.StatusRetry, Attempts: 2,
		PostURL: mainURL, PublishedAt: testNow.Add(-25 * time.Hour),
	}
	fixture.app.store.SetAt("guid-2", &owner, testNow.Add(-time.Hour))

	entry := fixture.poll()
	if entry.Status != state.StatusRetry || entry.PostURL != "" {
		t.Fatalf("replayed entry must wait for the unfinished owner: %#v", entry)
	}
	testutil.Equal(t, fixture.app.store.Items["guid-2"].Status, state.StatusFailedTerminal)
	fixture.assertCalls(1, 0)

	entry = fixture.poll()
	if entry.Status != state.StatusPosted || entry.ReplyURL != replyURL || entry.FoundOnX {
		t.Fatalf("replayed entry after the owner expired = %#v", entry)
	}
	fixture.assertCalls(2, 1)
	testutil.Equal(t, fixture.replyCalls.Load(), 1)
}

func TestPollSameArticleEntriesNeverWaitOnEachOther(t *testing.T) {
	const otherPost = "https://x.invalid/fixture/status/7"
	for _, tt := range []struct {
		name           string
		first, second  state.Entry
		posts, replies int32
	}{
		// Neither has a post on X, so the first publishes and the second finds its link reply.
		{"no posts yet", state.Entry{Status: state.StatusRetry, Attempts: 1}, state.Entry{Status: state.StatusRetry, Attempts: 1}, 1, 1},
		// An entry with its own post replies under it instead of waiting.
		{"own post", state.Entry{Status: state.StatusRetry, Attempts: 1, Format: state.FormatImage, PostURL: mainURL}, state.Entry{Status: state.StatusRetry, Attempts: 1, PostURL: otherPost, NextAttemptAt: testNow.Add(time.Hour)}, 0, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var inReplyTo []string
			fixture := newReplyTest(t, &pollTestOptions{
				initial: tt.first,
				extra:   []feed.Item{{GUID: "guid-2", Title: "Same article", Link: "/article", Published: testNow}},
				reply: func(w http.ResponseWriter, r *http.Request, _ feed.Item) {
					parent, _ := decodePayload(t, r)["inReplyTo"].(string)
					inReplyTo = append(inReplyTo, parent)
					testutil.JSON(t, w, http.StatusOK, map[string]any{"url": replyURL})
				},
			})
			second := tt.second
			second.Title, second.Link, second.PublishedAt = "Same article", fixture.item.Link, testNow
			fixture.app.store.SetAt("guid-2", &second, testNow.Add(-time.Hour))

			fixture.poll()
			testutil.Equal(t, fixture.postCalls.Load(), tt.posts)
			testutil.Equal(t, fixture.replyCalls.Load(), tt.replies)
			testutil.Equal(t, strings.Join(inReplyTo, " "), mainURL)
			testutil.Equal(t, fixture.app.store.Items["guid-1"].Status, state.StatusPosted)
		})
	}
}
