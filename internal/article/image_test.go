package article

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestFetchImage(t *testing.T) {
	tests := []struct {
		name, tag, mime, wantErr string
		imageStatus, size        int
	}{
		{name: "property", tag: `<meta property="og:image" content="%s">`, mime: "image/png", imageStatus: 200, size: 8},
		{name: "name and reversed attributes", tag: `<META content='%s' name='og:image'>`, mime: "image/jpeg; charset=binary", imageStatus: 200, size: 8},
		{name: "no image", tag: `<html></html>`, wantErr: "no og:image"},
		{name: "missing content", tag: `<meta property="og:image">`, wantErr: "no content"},
		{name: "unsupported media", tag: `<meta property="og:image" content="%s">`, mime: "text/html", imageStatus: 200, size: 8, wantErr: "unsupported image type"},
		{name: "missing image", tag: `<meta property="og:image" content="%s">`, imageStatus: 404, wantErr: "404"},
		{name: "empty image", tag: `<meta property="og:image" content="%s">`, mime: "image/png", imageStatus: 200, wantErr: "image is empty"},
		{name: "oversized image", tag: `<meta property="og:image" content="%s">`, mime: "image/png", imageStatus: 200, size: 5<<20 + 1, wantErr: "response exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/article" {
					imageURL := "http://" + html.EscapeString(r.Host) + "/share?width=100&amp;height=50"
					_, _ = w.Write([]byte(strings.ReplaceAll(tt.tag, "%s", imageURL)))
					return
				}
				if r.URL.Query().Get("height") != "50" {
					t.Errorf("HTML entities not decoded: %s", r.URL)
				}
				w.Header().Set("Content-Type", tt.mime)
				w.WriteHeader(tt.imageStatus)
				_, _ = fmt.Fprint(w, strings.Repeat("x", tt.size))
			})
			image, err := FetchImage(t.Context(), server.Client(), server.URL+"/article")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || image != nil {
					t.Fatalf("FetchImage() = %#v, %v; want %q", image, err, tt.wantErr)
				}
				return
			}
			testutil.NoError(t, err)
			wantMIME, _, _ := strings.Cut(tt.mime, ";")
			if string(image.Data) != strings.Repeat("x", tt.size) || image.MIME != wantMIME {
				t.Errorf("image = %#v", image)
			}
		})
	}
}
