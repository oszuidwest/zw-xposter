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

func TestFetchVideoEnclosures(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, media, want string
	}{
		{name: "no media"},
		{name: "image", media: `<enclosure type="image/jpeg" url="https://cdn.example/image.jpg"/>`},
		{name: "audio", media: `<enclosure type="audio/mpeg" url="https://cdn.example/audio.mp3"/>`},
		{name: "video", media: `<enclosure type="video/mp4" length="123" url=" https://cdn.example/video.mp4?a=1&amp;b=2 "/>`, want: "https://cdn.example/video.mp4?a=1&b=2"},
		{name: "MP4 preferred", media: `<enclosure type="video/quicktime" url="https://cdn.example/video.mov"/><enclosure type="video/mp4" url="https://cdn.example/video.mp4"/>`, want: "https://cdn.example/video.mp4"},
		{name: "unsupported video still takes priority over image", media: `<enclosure type="video/quicktime" url="https://cdn.example/video.mov"/>`, want: "https://cdn.example/video.mov"},
		{name: "empty URL", media: `<enclosure type="video/mp4" url=" "/>`},
		{name: "content is not an enclosure", media: `<content:encoded xmlns:content="http://purl.org/rss/1.0/modules/content/"><![CDATA[<video src="other.mp4">]]></content:encoded>`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := testutil.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(strings.ReplaceAll(sample, "</item>", tt.media+"</item>")))
			})
			items, err := Fetch(t.Context(), server.Client(), server.URL)
			testutil.NoError(t, err)
			if len(items) != 2 {
				t.Fatalf("items = %d, want 2", len(items))
			}
			for _, item := range items {
				testutil.Equal(t, item.VideoURL, tt.want)
			}
		})
	}
}
