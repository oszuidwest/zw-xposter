package dedupe

import (
	"strings"
	"testing"

	"github.com/oszuidwest/zw-xposter/internal/poster"
	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

const (
	link = "https://www.zuidwestupdate.nl/news/housing-development/"
	zws  = zeroWidthSpace
)

func TestFind(t *testing.T) {
	tests := []struct {
		name string
		post poster.Post
		want bool
	}{
		{"expanded link", poster.Post{URLs: []string{link}}, true},
		{"without www, with query", poster.Post{URLs: []string{"http://zuidwestupdate.nl/news/housing-development?utm_source=x"}}, true},
		{"no trailing slash, other case", poster.Post{URLs: []string{"https://www.ZuidWestUpdate.nl/news/Housing-Development"}}, true},
		{"dlvr.it zero-width text", poster.Post{Text: "Title www." + zws + "zuidwestupdate." + zws + "nl/news/housing-development/"}, true},
		{"dlvr.it text at end", poster.Post{Text: "Title www." + zws + "zuidwestupdate." + zws + "nl/news/housing-development"}, true},
		{"text with a longer slug", poster.Post{Text: "Part 2 www." + zws + "zuidwestupdate." + zws + "nl/news/housing-development-part-2/"}, false},
		{"text with a longer domain", poster.Post{Text: "https://notzuidwestupdate.nl/news/housing-development/"}, false},
		{"text with another subdomain", poster.Post{Text: "https://other.zuidwestupdate.nl/news/housing-development/"}, false},
		{"text with a longer path", poster.Post{Text: link + "part-2/"}, false},
		{"text with an embedded link", poster.Post{Text: "https://example.nl/" + link}, false},
		{"text with a query", poster.Post{Text: link + "?utm_source=x"}, true},
		{"text with parentheses", poster.Post{Text: "Read (" + link + ")"}, true},
		{"text with punctuation", poster.Post{Text: "Read https://zuidwestupdate.nl/news/housing-development."}, true},
		{"text with mixed case and no scheme", poster.Post{Text: "Read\tWWW.ZuidWestUpdate.nl/news/Housing-Development"}, true},
		{"text with a link in another site's query", poster.Post{Text: "https://example.nl/?url=" + link}, false},
		{"text with an invalid link before the match", poster.Post{Text: "https://other.zuidwestupdate.nl/news/housing-development/ " + link}, true},
		{"other article", poster.Post{URLs: []string{"https://www.zuidwestupdate.nl/news/something-else/"}}, false},
		{"article whose slug extends this one", poster.Post{URLs: []string{"https://www.zuidwestupdate.nl/news/housing-development-part-2/"}}, false},
		{"other site, same path", poster.Post{URLs: []string{"https://example.nl/news/housing-development/"}}, false},
		{"homepage", poster.Post{URLs: []string{"https://www.zuidwestupdate.nl/"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testutil.Equal(t, Find(link, []poster.Post{tt.post}) != nil, tt.want)
		})
	}
}

func TestRepliesTo(t *testing.T) {
	const parent = "https://x.com/fixture_account/status/101"
	posts := []poster.Post{
		{URL: parent},
		{URL: "https://x.com/fixture_account/status/102", ReplyTo: "https://x.com/Fixture_Account/status/101"},
		{URL: "https://x.com/fixture_account/status/103", ReplyTo: "https://x.com/fixture_account/status/100"},
		{URL: "https://x.com/fixture_account/status/104", ReplyTo: "https://x.com/fixture_account/status/1010"},
	}
	var got []string
	for _, reply := range RepliesTo(parent, posts) {
		got = append(got, reply.URL)
	}
	testutil.Equal(t, strings.Join(got, " "), "https://x.com/fixture_account/status/102")
	testutil.Equal(t, len(RepliesTo("https://x.com/fixture_account", posts)), 0)
}

func TestSameLink(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{link, "http://zuidwestupdate.nl/NEWS/housing-development?utm=x", true},
		{link, "https://www.zuidwestupdate.nl/news/other/", false},
		{link, "https://example.nl/news/housing-development/", false},
		{"not a link", "not a link", false},
	} {
		t.Run(tt.b, func(t *testing.T) {
			testutil.Equal(t, SameLink(tt.a, tt.b), tt.want)
		})
	}
}

func TestFindReturnsLinkedReply(t *testing.T) {
	reply := poster.Post{URL: "https://x.com/fixture_account/status/102", URLs: []string{link}, ReplyTo: "https://x.com/fixture_account/status/101"}
	got := Find(link, []poster.Post{{URL: "https://x.com/fixture_account/status/101", Text: "Headline"}, reply})
	if got == nil || got.ReplyTo != reply.ReplyTo {
		t.Fatalf("Find() = %#v, want the reply with its parent", got)
	}
}

func TestSamePost(t *testing.T) {
	const post = "https://x.com/fixture_account/status/101"
	testutil.Equal(t, SamePost(post, "https://x.com/Fixture_Account/status/101"), true)
	testutil.Equal(t, SamePost(post, "https://x.com/fixture_account/status/1010"), false)
	testutil.Equal(t, SamePost("", ""), false)
}

func TestNewerPost(t *testing.T) {
	testutil.Equal(t, NewerPost("https://x.com/a/status/1010", "https://x.com/a/status/999"), true)
	testutil.Equal(t, NewerPost("https://x.com/a/status/101", "https://x.com/a/status/102"), false)
	testutil.Equal(t, NewerPost("https://x.com/a/status/101", "https://x.com/a/status/101"), false)
}
