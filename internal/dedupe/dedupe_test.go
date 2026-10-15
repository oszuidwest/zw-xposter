package dedupe

import (
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

func TestFindHeadline(t *testing.T) {
	const (
		headline = "Gemeente investeert in fietspaden & bruggen"
		mainURL  = "https://x.com/fixture_account/status/101"
	)
	main := poster.Post{URL: mainURL, Text: "Gemeente investeert in fietspaden &amp; bruggen https://t.co/media"}
	tests := []struct {
		name  string
		posts []poster.Post
		want  string
	}{
		{"headline with media link", []poster.Post{main}, mainURL},
		{"spacing differs", []poster.Post{{URL: mainURL, Text: " Gemeente investeert in\nfietspaden & bruggen "}}, mainURL},
		{"other headline", []poster.Post{{URL: mainURL, Text: "Gemeente investeert in fietspaden"}}, ""},
		{"headline with more text", []poster.Post{{URL: mainURL, Text: headline + " vandaag"}}, ""},
		{"reply with the headline", []poster.Post{{URL: mainURL, Text: headline, ReplyTo: "https://x.com/fixture_account/status/100"}}, ""},
		{"already has a link reply", []poster.Post{main, {
			URL: "https://x.com/fixture_account/status/102", URLs: []string{"https://example.invalid/other"},
			ReplyTo: "https://x.com/Fixture_Account/status/101",
		}}, ""},
		{"reply without a link does not claim the post", []poster.Post{main, {
			URL: "https://x.com/fixture_account/status/102", Text: "Update", ReplyTo: mainURL,
		}}, mainURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ""
			if post := FindHeadline(headline, tt.posts); post != nil {
				got = post.URL
			}
			testutil.Equal(t, got, tt.want)
		})
	}
	testutil.Equal(t, FindHeadline(" ", []poster.Post{{URL: mainURL}}), (*poster.Post)(nil))
}

func TestFindReturnsLinkedReply(t *testing.T) {
	reply := poster.Post{URL: "https://x.com/fixture_account/status/102", URLs: []string{link}, ReplyTo: "https://x.com/fixture_account/status/101"}
	got := Find(link, []poster.Post{{URL: "https://x.com/fixture_account/status/101", Text: "Headline"}, reply})
	if got == nil || got.ReplyTo != reply.ReplyTo {
		t.Fatalf("Find() = %#v, want the reply with its parent", got)
	}
}
