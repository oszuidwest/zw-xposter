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
