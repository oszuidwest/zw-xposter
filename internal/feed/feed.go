// Package feed fetches and parses the WordPress RSS feed.
package feed

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/safehttp"
)

const maxBodySize = 2 << 20

// Item is a single article from the feed.
type Item struct {
	GUID      string
	Title     string
	Link      string
	Published time.Time
}

type rss struct {
	Items []struct {
		GUID    string `xml:"guid"`
		Title   string `xml:"title"`
		Link    string `xml:"link"`
		PubDate string `xml:"pubDate"`
	} `xml:"channel>item"`
}

// Fetch downloads the feed and returns its items oldest first.
func Fetch(ctx context.Context, client *http.Client, url string) ([]Item, error) {
	body, _, err := safehttp.Get(ctx, client, url, maxBodySize)
	if err != nil {
		return nil, fmt.Errorf("fetch feed: %w", err)
	}

	var doc rss
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}

	items := make([]Item, 0, len(doc.Items))
	for _, raw := range doc.Items {
		published, err := time.Parse(time.RFC1123Z, strings.TrimSpace(raw.PubDate))
		if err != nil {
			slog.Warn("skipping feed item with invalid pubDate", "guid", strings.TrimSpace(raw.GUID), "pub_date", raw.PubDate, "error", err)
			continue
		}
		item := Item{
			GUID:      strings.TrimSpace(raw.GUID),
			Title:     strings.TrimSpace(raw.Title),
			Link:      strings.TrimSpace(raw.Link),
			Published: published,
		}
		if item.GUID == "" {
			item.GUID = item.Link
		}
		items = append(items, item)
	}

	// Post in publication order; WordPress lists newest first.
	slices.SortStableFunc(items, func(a, b Item) int { return a.Published.Compare(b.Published) })
	return items, nil
}
