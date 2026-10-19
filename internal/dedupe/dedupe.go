// Package dedupe recognizes articles that are already posted on X.
package dedupe

import (
	"net/url"
	"strings"

	"github.com/oszuidwest/zw-xposter/internal/poster"
)

// Find returns the post that links to the article at link, matched by SameLink, or nil.
// Text links also match after stripping dlvr.it's zero-width spaces.
func Find(link string, posts []poster.Post) *poster.Post {
	for i := range posts {
		post := &posts[i]
		for _, u := range post.URLs {
			if SameLink(link, u) {
				return post
			}
		}
		text := strings.ReplaceAll(post.Text, zeroWidthSpace, "")
		for word := range strings.FieldsSeq(text) {
			word = strings.Trim(word, `()[]<>"'.,;!?`)
			if !strings.Contains(word, "://") {
				word = "https://" + word
			}
			if SameLink(link, word) {
				return post
			}
		}
	}
	return nil
}

const zeroWidthSpace = "\u200b"

// RepliesTo returns the replies to the post at parent.
func RepliesTo(parent string, posts []poster.Post) []poster.Post {
	var replies []poster.Post
	for _, post := range posts {
		if SamePost(parent, post.ReplyTo) {
			replies = append(replies, post)
		}
	}
	return replies
}

// SamePost reports whether a and b are status URLs of the same post, matched by status ID so
// handle case and spelling cannot matter.
func SamePost(a, b string) bool {
	id := statusID(a)
	return id != "" && id == statusID(b)
}

// NewerPost reports whether status URL a is a later post than b; X status IDs grow over time.
func NewerPost(a, b string) bool {
	idA, idB := statusID(a), statusID(b)
	return len(idA) > len(idB) || len(idA) == len(idB) && idA > idB
}

// SameLink reports whether a and b link to the same article: host and path match, ignoring case,
// scheme, www, queries and edge slashes.
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
