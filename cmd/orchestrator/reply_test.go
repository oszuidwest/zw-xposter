package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/config"
	"github.com/oszuidwest/zw-xposter/internal/feed"
	"github.com/oszuidwest/zw-xposter/internal/notify"
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

// addArticleEntry stores another feed entry, for the fixture's article unless entry has a link.
func addArticleEntry(fixture *pollTest, guid string, entry *state.Entry) {
	entry.Link = cmp.Or(entry.Link, fixture.item.Link)
	if entry.PublishedAt.IsZero() {
		entry.PublishedAt = testNow
	}
	fixture.app.store.SetAt(guid, entry, testNow.Add(-time.Hour))
}

func TestPollOtherGUIDWaitsWhileOwnerCanStillReply(t *testing.T) {
	fixture := newReplyTest(t, &pollTestOptions{
		extra: []feed.Item{{GUID: "guid-2", Title: "Same article", Link: "/article", Published: testNow}},
		// The first entry's reply fails in the same poll, so X shows no link yet.
		reply: func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
			testutil.JSON(t, w, http.StatusInternalServerError, map[string]any{"error": "reply button missing", "clicked": false})
		},
	})
	if first := fixture.poll(); first.Status != state.StatusRetry || first.PostURL != mainURL || first.ReplyURL != "" {
		t.Fatalf("first = %#v", first)
	}
	if second, ok := fixture.app.store.Items["guid-2"]; ok {
		t.Fatalf("second must stay untouched while the first can reply: %#v", second)
	}
	fixture.assertCalls(1, 1)
	testutil.Equal(t, fixture.replyCalls.Load(), 1)
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
		// Its lost response may hide a headline already on X.
		{"uncertain owner without a post", state.Entry{Status: state.StatusUncertain, Attempts: 1}, true},
		// The owner gave up with an alert; the new entry publishes the article itself.
		{"expired owner", state.Entry{Status: state.StatusFailedTerminal, Attempts: 3, PostURL: mainURL}, false},
		{"other article", state.Entry{Status: state.StatusRetry, Attempts: 1, PostURL: mainURL, Link: "https://example.invalid/other"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newReplyTest(t, &pollTestOptions{})
			addArticleEntry(fixture, "guid-0", &tt.owner)
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
		initial:   state.Entry{Status: state.StatusRetry},
		// The owner is listed but past MAX_AGE; the replayed entry comes first in the feed,
		// so it waits before the owner expires.
		extra: []feed.Item{{GUID: "guid-2", Title: "Same article", Link: "/article", Published: testNow.Add(-25 * time.Hour)}},
	})
	testutil.NoError(t, fixture.app.replay(t.Context(), fixture.item.GUID))
	addArticleEntry(fixture, "guid-2", &state.Entry{Status: state.StatusRetry, Attempts: 2, PostURL: mainURL})

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
	for _, tt := range []struct {
		name          string
		first, second state.Entry
		posts         int32
	}{
		// Neither has a post on X, so the first publishes and the second finds its link reply.
		{"no posts yet", state.Entry{Status: state.StatusRetry, Attempts: 1}, state.Entry{Status: state.StatusRetry, Attempts: 1}, 1},
		// An entry with its own post replies under it instead of waiting; the other entry's reply retry is not due yet.
		{"own post", state.Entry{Status: state.StatusRetry, Attempts: 1, Format: state.FormatImage, PostURL: mainURL}, state.Entry{Status: state.StatusRetry, Attempts: 1, PostURL: otherPost, NextAttemptAt: testNow.Add(time.Hour)}, 0},
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
			addArticleEntry(fixture, "guid-2", &tt.second)

			fixture.poll()
			fixture.assertCalls(1, tt.posts)
			// Exactly one reply, under the first entry's post.
			testutil.Equal(t, strings.Join(inReplyTo, " "), mainURL)
			testutil.Equal(t, fixture.app.store.Items["guid-1"].Status, state.StatusPosted)
		})
	}
}

func TestPendingPostNeverMakesUncertainEntriesWait(t *testing.T) {
	fixture := newReplyTest(t, &pollTestOptions{})
	addArticleEntry(fixture, "guid-0", &state.Entry{Status: state.StatusUncertain, Attempts: 1})
	entry := state.Entry{Status: state.StatusRetry, Attempts: 1, Link: fixture.item.Link}
	testutil.Equal(t, fixture.app.pendingPost(fixture.item.GUID, &entry), "guid-0")
	// An uncertain entry is reconciled by its own link and must never wait, or two could wait on each other.
	entry.Status = state.StatusUncertain
	testutil.Equal(t, fixture.app.pendingPost(fixture.item.GUID, &entry), "")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// pollMails runs one poll with alert e-mail enabled and returns each mail as subject and body.
func pollMails(t *testing.T, fixture *pollTest) []string {
	t.Helper()
	var mails []string
	mail := httptest.NewTestServer(t, graphMailHandler(t, func(subject, body string) { mails = append(mails, subject+"\n"+body) }))
	// Send mail to the test server and only the poster to the network; the global override forbids t.Parallel.
	posterHost := strings.TrimPrefix(fixture.app.cfg.PosterURL, "http://")
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case posterHost:
			return original.RoundTrip(r)
		case "login.microsoftonline.com", "graph.microsoft.com":
			return mail.Client().Transport.RoundTrip(r)
		}
		t.Errorf("unexpected request to %s", r.URL)
		return nil, errors.New("unexpected request")
	})
	cfg := config.GraphConfig{TenantID: "offline", ClientID: "offline", ClientSecret: "offline",
		FromAddress: "sender@example.invalid", Recipients: []string{"ops@example.invalid"}}
	fixture.app.alerts = notify.New(&cfg, 24*time.Hour)
	// A failed poll must not leave delivery running against another test's transport.
	defer fixture.app.alerts.Close()
	fixture.poll()
	fixture.app.alerts.Close() // Drain delivery before reading the mails.
	fixture.app.alerts = nil
	return mails
}

// assertMails checks each mail's subject and that its body contains the wanted lines.
func assertMails(t *testing.T, mails []string, want ...[]string) {
	t.Helper()
	if len(mails) != len(want) {
		t.Fatalf("mails = %q, want %d", mails, len(want))
	}
	for i, lines := range want {
		subject, _, _ := strings.Cut(mails[i], "\n")
		testutil.Equal(t, subject, "[ALERT] "+lines[0]+" - ZuidWest X Poster")
		for _, line := range lines[1:] {
			if !strings.Contains(mails[i], line) {
				t.Errorf("mail %d = %q, want %q", i, mails[i], line)
			}
		}
	}
}

const (
	otherPost   = "https://x.invalid/fixture/status/7"
	gaveUp      = "Article posting failed permanently"
	postedAgain = "Article posted again after an earlier attempt gave up"
)

func TestPollRepublishAlertsOnceTheNewHeadlineIsOnX(t *testing.T) {
	// The alert must not wait for the link reply, which may never be confirmed.
	fixture := newReplyTest(t, &pollTestOptions{reply: func(w http.ResponseWriter, _ *http.Request, _ feed.Item) {
		testutil.JSON(t, w, http.StatusInternalServerError, map[string]any{"error": "reply button missing", "clicked": false})
	}})
	// The unlisted owner expires with its alert first, so the new entry publishes the article itself.
	addArticleEntry(fixture, "guid-0", &state.Entry{Status: state.StatusRetry, Attempts: 2, PostURL: otherPost, PublishedAt: testNow.Add(-25 * time.Hour)})
	mails := pollMails(t, fixture)
	testutil.Equal(t, fixture.app.store.Items[fixture.item.GUID].PostURL, mainURL)
	assertMails(t, mails, []string{gaveUp, "Condition: workflow:failed_terminal:guid-0"},
		[]string{postedAgain, "Condition: workflow:posted-again:guid-1", "New post: " + mainURL, "Earlier attempt: guid-0, post " + otherPost, "This replaces the earlier alert for guid-0."})
}

func TestPollRepublishAlertNamesEveryEarlierHeadline(t *testing.T) {
	fixture := newReplyTest(t, &pollTestOptions{})
	// The first entry gave up with a post, the second expired while uncertain; neither is listed.
	// guid-a links the article with a tracking query; guid-b has the error text of earlier releases.
	addArticleEntry(fixture, "guid-a", &state.Entry{Status: state.StatusFailedTerminal, Attempts: 2, PostURL: otherPost, Link: fixture.item.Link + "/?utm_source=x"})
	addArticleEntry(fixture, "guid-b", &state.Entry{Status: state.StatusFailedTerminal, Attempts: 1, LastError: "retry window expired while the outcome was uncertain; checked manually"})
	// An entry for another article must not be named.
	addArticleEntry(fixture, "guid-c", &state.Entry{Status: state.StatusUncertain, Attempts: 1, Link: fixture.item.Link + "-other"})
	assertMails(t, pollMails(t, fixture), []string{postedAgain,
		"GUID: guid-1\nTitle: Synthetic article\nLink: " + fixture.item.Link + "\nNew post: " + mainURL + "\nEarlier attempt: guid-a, post " + otherPost + ": delete it; do not add the article link to it.\nEarlier attempt: guid-b, outcome unknown: if X shows another post with this headline besides the new post, delete it; do not add the article link to it.",
		"This replaces the earlier alert for guid-a and guid-b."})
}

func TestPollNoRepublishAlertWithoutEarlierHeadline(t *testing.T) {
	fixture := newReplyTest(t, &pollTestOptions{})
	// Every attempt failed before the click, so no headline of the other entry reached X.
	addArticleEntry(fixture, "guid-0", &state.Entry{Status: state.StatusRetry, Attempts: 3, LastError: "image upload failed", PublishedAt: testNow.Add(-25 * time.Hour)})
	assertMails(t, pollMails(t, fixture), []string{gaveUp, "Condition: workflow:failed_terminal:guid-0"})
	testutil.Equal(t, fixture.app.store.Items[fixture.item.GUID].Status, state.StatusPosted)
}

func TestPollRepublishAlertForOwnLostPostFoundOnX(t *testing.T) {
	const ownPost = "https://x.invalid/fixture/status/9"
	lost := state.Entry{Status: state.StatusUncertain, Attempts: 1, Format: state.FormatText, UpdatedAt: testNow.Add(-time.Hour)}
	for _, tt := range []struct {
		name    string
		initial state.Entry
		found   map[string]any
		want    [][]string
	}{
		{"own post", lost, map[string]any{"id": "9", "url": ownPost}, [][]string{{postedAgain, "New post: " + ownPost, "Earlier attempt: guid-0, post " + otherPost}}},
		// Attempts that failed before the click put nothing on X, so the found post is someone else's.
		{"post after a failed attempt", state.Entry{Status: state.StatusRetry, Attempts: 1, Format: state.FormatText}, map[string]any{"id": "9", "url": ownPost}, nil},
		// A link reply someone added under the earlier post completes that post instead.
		// X may spell the handle differently from the saved post URL.
		{"reply under the earlier post", lost, map[string]any{"id": "8", "url": "https://x.invalid/fixture/status/8", "replyTo": "https://x.invalid/Fixture/status/7"}, nil},
		// Without an attempt of its own, a post found on X was posted by someone else.
		{"post of a new entry", state.Entry{}, map[string]any{"id": "9", "url": ownPost}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newPollTest(t, &pollTestOptions{
				initial: tt.initial,
				recent: func(w http.ResponseWriter, _ *http.Request, item feed.Item) {
					tt.found["text"], tt.found["urls"] = item.Title+" "+item.Link, []string{item.Link}
					testutil.JSON(t, w, http.StatusOK, map[string]any{"posts": []any{tt.found}, "complete": true})
				},
			})
			addArticleEntry(fixture, "guid-0", &state.Entry{Status: state.StatusFailedTerminal, Attempts: 2, PostURL: otherPost})
			assertMails(t, pollMails(t, fixture), tt.want...)
			testutil.Equal(t, fixture.app.store.Items[fixture.item.GUID].FoundOnX, true)
			fixture.assertCalls(1, 0)
		})
	}
}

func TestPollExpiredEntryPointsToSiblingHeadline(t *testing.T) {
	expired := testNow.Add(-25 * time.Hour)
	onX := "covered by feed entry guid-0, whose headline is on X as " + otherPost + " without its link; do not post this article again"
	mayBeOnX := "covered by feed entry guid-0, whose headline may be on X without its link; post this article only if X shows no post with its headline"
	withPost := state.Entry{Status: state.StatusRetry, Attempts: 2, PostURL: otherPost}
	for _, tt := range []struct {
		name            string
		listed          bool
		entry, owner    state.Entry
		status, wantErr string
	}{
		// The owner comes later in the feed, so it is still pending when the waiter expires.
		{"owner later in the feed", true, state.Entry{}, withPost, state.StatusMissed, onX},
		{"waiter failed before the click", false, state.Entry{Status: state.StatusRetry, Attempts: 1, LastError: "image upload failed"}, withPost,
			state.StatusFailedTerminal, "image upload failed; " + onX},
		{"uncertain waiter", false, state.Entry{Status: state.StatusUncertain, Attempts: 1, LastError: "lost response"}, withPost,
			state.StatusFailedTerminal, uncertainExpired + "; the post may already be on X and must be checked manually: lost response; " + onX},
		// A replayed entry has no error of its own yet.
		{"replayed waiter", false, state.Entry{Status: state.StatusRetry}, withPost, state.StatusFailedTerminal, onX},
		// A waiter with its own post reports that post, not the other one.
		{"waiter with its own post", false, state.Entry{Status: state.StatusRetry, Attempts: 1, PostURL: mainURL}, withPost,
			state.StatusFailedTerminal, "the post is on X, but its link reply may be missing; check it and add the article link manually"},
		{"other article", false, state.Entry{}, state.Entry{Status: state.StatusUncertain, Attempts: 1, Link: "https://example.invalid/other"}, state.StatusMissed, ""},
		{"uncertain owner later in the feed", true, state.Entry{}, state.Entry{Status: state.StatusUncertain, Attempts: 1}, state.StatusMissed, mayBeOnX},
		{"owner later in the feed never reached X", true, state.Entry{}, state.Entry{Status: state.StatusRetry, Attempts: 3}, state.StatusMissed, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := &pollTestOptions{published: expired, initial: tt.entry}
			if tt.listed {
				opts.extra = []feed.Item{{GUID: "guid-0", Title: "Same article", Link: "/article", Published: expired}}
			}
			fixture := newReplyTest(t, opts)
			tt.owner.PublishedAt = expired
			addArticleEntry(fixture, "guid-0", &tt.owner)
			entry := fixture.poll()
			testutil.Equal(t, entry.Status, tt.status)
			testutil.Equal(t, entry.LastError, tt.wantErr)
			fixture.assertCalls(0, 0)
		})
	}
}

func TestPollExpiredEntryPrefersSiblingWithAKnownPost(t *testing.T) {
	expired := testNow.Add(-25 * time.Hour)
	fixture := newReplyTest(t, &pollTestOptions{published: expired})
	addArticleEntry(fixture, "guid-a", &state.Entry{Status: state.StatusUncertain, Attempts: 1, PublishedAt: expired})
	addArticleEntry(fixture, "guid-b", &state.Entry{Status: state.StatusRetry, Attempts: 2, PostURL: otherPost, PublishedAt: expired})
	// The newest post is the one still on X: its republish alert asked for the older one to be deleted.
	const newestPost = "https://x.invalid/fixture/status/9"
	addArticleEntry(fixture, "guid-c", &state.Entry{Status: state.StatusRetry, Attempts: 2, PostURL: newestPost, PublishedAt: expired})
	entry := fixture.poll()
	testutil.Equal(t, entry.LastError, "covered by feed entry guid-c, whose headline is on X as "+newestPost+" without its link; do not post this article again")
}

func TestPollEarlierAttemptWithLinkInPostHoldsNothingBack(t *testing.T) {
	for _, tt := range []struct {
		name        string
		linkInReply bool
		owner       state.Entry
	}{
		{"link in the post", false, state.Entry{Status: state.StatusUncertain, Attempts: 1, Format: state.FormatImage}},
		{"text post", true, state.Entry{Status: state.StatusUncertain, Attempts: 1, Format: state.FormatText}},
		{"expired text post", true, state.Entry{Status: state.StatusFailedTerminal, Attempts: 1, Format: state.FormatText,
			LastError: uncertainExpired + "; the post may already be on X and must be checked manually"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newReplyTest(t, &pollTestOptions{})
			fixture.app.cfg.LinkInReply = tt.linkInReply
			// The earlier attempt carried the link, so the complete X check already shows it is not on X.
			addArticleEntry(fixture, "guid-0", &tt.owner)
			assertMails(t, pollMails(t, fixture))
			testutil.Equal(t, fixture.app.store.Items[fixture.item.GUID].Status, state.StatusPosted)
			fixture.assertCalls(1, 1)
		})
	}
}
