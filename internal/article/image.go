// Package article retrieves the share image of an article.
package article

import (
	"context"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/http"
	"path"
	"regexp"

	"github.com/oszuidwest/zw-xposter/internal/safehttp"
)

// maxImageSize is the largest image X accepts for a regular post.
const maxImageSize = 5 << 20

// Image is a downloaded share image.
type Image struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data []byte `json:"data"` // encoding/json writes []byte as base64.
}

var ogImage = regexp.MustCompile(`(?is)<meta\s+[^>]*(?:property|name)=["']og:image["'][^>]*>`)
var contentAttr = regexp.MustCompile(`(?is)content=["']([^"']+)["']`)

// FetchImage downloads the og:image of the article at pageURL.
func FetchImage(ctx context.Context, client *http.Client, pageURL string) (*Image, error) {
	page, _, err := safehttp.Get(ctx, client, pageURL, 2<<20)
	if err != nil {
		return nil, fmt.Errorf("fetch article: %w", err)
	}

	tag := ogImage.Find(page)
	if tag == nil {
		return nil, errors.New("article has no og:image")
	}
	m := contentAttr.FindSubmatch(tag)
	if m == nil {
		return nil, errors.New("og:image has no content")
	}
	imageURL := html.UnescapeString(string(m[1]))

	img, contentType, err := safehttp.Get(ctx, client, imageURL, maxImageSize)
	if err != nil {
		return nil, fmt.Errorf("fetch image %s: %w", imageURL, err)
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch mediaType {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
	default:
		return nil, fmt.Errorf("unsupported image type %q", contentType)
	}

	return &Image{Name: path.Base(imageURL), MIME: mediaType, Data: img}, nil
}
