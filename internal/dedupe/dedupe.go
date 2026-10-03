// Package dedupe recognizes articles that are already posted on X.
package dedupe

import (
	"net/url"
	"strings"

	"github.com/oszuidwest/zw-xposter/internal/poster"
)

// Find returns the post that links to the article at link, or nil.
//
// Expanded URLs match on host and path, ignoring case, scheme, www, queries and edge slashes.
// Text matching searches host/path after stripping dlvr.it's zero-width spaces.
func Find(link string, posts []poster.Post) *poster.Post {
	host, path, ok := split(link)
	if !ok {
		return nil
	}
	for i := range posts {
		post := &posts[i]
		for _, u := range post.URLs {
			if h, p, ok := split(u); ok && h == host && p == path {
				return post
			}
		}
		text := strings.ToLower(strings.ReplaceAll(post.Text, zeroWidthSpace, ""))
		if containsLink(text, host+"/"+path) {
			return post
		}
	}
	return nil
}

const zeroWidthSpace = "\u200b"

// containsLink requires a separator or end of text after link to reject longer slugs.
func containsLink(text, link string) bool {
	for start := 0; ; {
		i := strings.Index(text[start:], link)
		if i < 0 {
			return false
		}
		end := start + i + len(link)
		if end == len(text) || strings.ContainsAny(text[end:end+1], "/?# \n\t") {
			return true
		}
		start = end
	}
}

// split lowercases the host and escaped path, stripping www and surrounding slashes.
// Empty paths are rejected so homepages cannot match articles.
func split(raw string) (host, path string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", "", false
	}
	host = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path = strings.ToLower(strings.Trim(u.EscapedPath(), "/"))
	if path == "" {
		return "", "", false
	}
	return host, path, true
}
