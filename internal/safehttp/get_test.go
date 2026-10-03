package safehttp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestGet(t *testing.T) {
	server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		default:
			w.Header().Set("Content-Type", " image/png ")
			_, _ = w.Write([]byte(strings.Repeat("x", 8)))
		}
	})

	body, contentType, err := Get(t.Context(), server.Client(), server.URL+"/image", 8)
	testutil.NoError(t, err)
	if len(body) != 8 || contentType != "image/png" {
		t.Errorf("Get() = %d bytes, %q; want 8 bytes, image/png", len(body), contentType)
	}

	tests := []struct {
		name, path string
		limit      int64
		wantErr    string
	}{
		{name: "over the limit", path: "/image", limit: 7, wantErr: "response exceeds 7 bytes"},
		{name: "non-200", path: "/missing", limit: 8, wantErr: "status 404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := Get(t.Context(), server.Client(), server.URL+tt.path, tt.limit)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || body != nil {
				t.Fatalf("Get() = %d bytes, error %v; want no body and %q", len(body), err, tt.wantErr)
			}
		})
	}
}
