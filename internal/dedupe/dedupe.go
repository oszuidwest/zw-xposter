// Package dedupe recognizes articles that are already posted on X.
package dedupe

import (
	"html"
	"net/url"
	"regexp"
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

// FindHeadline returns an own top-level post whose text is headline, or nil.
//
// It finds headline-only posts whose link reply is missing. Posts that already
// have a linked reply belong to an article and are skipped; X's media links and
// HTML escapes are ignored.
func FindHeadline(headline string, posts []poster.Post) *poster.Post {
	want := plainText(headline)
	if want == "" {
		return nil
	}
	linked := make(map[string]bool)
	for _, post := range posts {
		if post.ReplyTo != "" && len(post.URLs) > 0 {
			linked[statusID(post.ReplyTo)] = true
		}
	}
	for i := range posts {
		post := &posts[i]
		if post.ReplyTo == "" && !linked[statusID(post.URL)] && plainText(post.Text) == want {
			return post
		}
	}
	return nil
}

var shortLink = regexp.MustCompile(`https://t\.co/\S+`)

func plainText(text string) string {
	text = shortLink.ReplaceAllString(html.UnescapeString(text), "")
	return strings.Join(strings.Fields(text), " ")
}

// statusID returns the post ID in a status URL, so handle case and spelling cannot matter.
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
