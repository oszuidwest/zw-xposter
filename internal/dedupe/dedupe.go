// Package dedupe recognizes articles that are already posted on X.
package dedupe

import (
	"net/url"
	"strings"

	"github.com/oszuidwest/zw-xposter/internal/poster"
)

// Find returns the post that links to the article at link, or nil.
//
// URLs match on host and path, ignoring case, scheme, www, queries and edge slashes.
// Text links also match after stripping dlvr.it's zero-width spaces.
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
		text := strings.ReplaceAll(post.Text, zeroWidthSpace, "")
		for word := range strings.FieldsSeq(text) {
			word = strings.Trim(word, `()[]<>"'.,;!?`)
			if !strings.Contains(word, "://") {
				word = "https://" + word
			}
			if h, p, ok := split(word); ok && h == host && p == path {
				return post
			}
		}
	}
	return nil
}

const zeroWidthSpace = "\u200b"

// RepliesTo returns the replies to the post at parent, matched by status ID so handle case and spelling cannot matter.
func RepliesTo(parent string, posts []poster.Post) []poster.Post {
	id := statusID(parent)
	if id == "" {
		return nil
	}
	var replies []poster.Post
	for _, post := range posts {
		if statusID(post.ReplyTo) == id {
			replies = append(replies, post)
		}
	}
	return replies
}

// SamePost reports whether a and b are status URLs of the same post, matched by status ID.
func SamePost(a, b string) bool {
	id := statusID(a)
	return id != "" && id == statusID(b)
}

// SameLink reports whether a and b link to the same article, matched as Find matches URLs.
func SameLink(a, b string) bool {
	hostA, pathA, okA := split(a)
	hostB, pathB, okB := split(b)
	return okA && okB && hostA == hostB && pathA == pathB
}

// statusID returns the post ID in a status URL.
func statusID(raw string) string {
	_, id, ok := strings.Cut(raw, "/status/")
	if !ok {
		return ""
	}
	id, _, _ = strings.Cut(id, "/")
	return id
}

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
