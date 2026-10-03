package feed

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

const sample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
<item>
	<title>Newest &#8216;article&#8217;</title>
	<link>https://example.nl/b/</link>
	<pubDate>Thu, 01 Oct 2026 12:11:34 +0000</pubDate>
	<guid isPermaLink="false">https://example.nl/?p=2</guid>
</item>
<item>
	<title>Older article</title>
	<link>https://example.nl/a/</link>
	<pubDate>Thu, 01 Oct 2026 11:00:17 +0000</pubDate>
	<guid isPermaLink="false">https://example.nl/?p=1</guid>
</item>
</channel></rss>`

func TestFetch(t *testing.T) {
	for _, tt := range []struct {
		name, body, wantErr string
		want                []string
	}{
		{
			name: "oldest first with decoded titles", body: sample,
			want: []string{"https://example.nl/?p=1: Older article", "https://example.nl/?p=2: Newest ‘article’"},
		},
		{
			name: "invalid publication date",
			body: strings.Replace(sample, "Thu, 01 Oct 2026 11:00:17 +0000", "not a date", 1),
			want: []string{"https://example.nl/?p=2: Newest ‘article’"},
		},
		{name: "oversized body", body: strings.Repeat("x", maxBodySize+1), wantErr: "fetch feed: response exceeds"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := testutil.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				if _, err := w.Write([]byte(tt.body)); err != nil {
					t.Errorf("write response: %v", err)
				}
			})
			items, err := Fetch(t.Context(), server.Client(), server.URL)
			testutil.ErrorContains(t, err, tt.wantErr)
			got := make([]string, 0, len(items))
			for _, item := range items {
				got = append(got, item.GUID+": "+item.Title)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("articles = %q, want %q", got, tt.want)
			}
		})
	}
}
