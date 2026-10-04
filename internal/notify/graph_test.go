package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestGraphClientGetsTokenAndSendsMail(t *testing.T) {
	var request any
	server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
		testutil.Equal(t, r.Method, http.MethodPost)
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			if r.Form.Get("client_secret") != "secret" || r.Form.Get("scope") != "https://graph.microsoft.com/.default" ||
				r.Form.Get("client_id") != "client" || r.Form.Get("grant_type") != "client_credentials" {
				t.Errorf("unexpected token form: %v", r.Form)
			}
			testutil.JSON(t, w, http.StatusOK, map[string]any{"access_token": "synthetic-token", "expires_in": 3600})
		case "/users/sender@example.com/sendMail":
			testutil.Equal(t, r.Header.Get("Content-Type"), "application/json")
			if r.Header.Get("Authorization") != "Bearer synthetic-token" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode mail request: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	})

	client := &graphClient{
		clientID: "client", clientSecret: "secret", fromAddress: "sender@example.com",
		tokenURL: server.URL + "/token", baseURL: server.URL, httpClient: server.Client(),
	}
	testutil.NoError(t, client.SendMail(t.Context(), []string{"ops@example.com"}, "subject", "body"))
	var want any
	if err := json.Unmarshal([]byte(`{
		"message": {
			"subject": "subject",
			"body": {"contentType": "Text", "content": "body"},
			"toRecipients": [{"emailAddress": {"address": "ops@example.com"}}]
		}
	}`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request, want) {
		t.Errorf("mail JSON = %#v, want %#v", request, want)
	}
}

func TestGraphClientRetriesTransientFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		requests, tokens := 0, 0
		started := time.Now()
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				tokens++
				testutil.JSON(t, w, http.StatusOK, map[string]any{"access_token": fmt.Sprintf("token-%d", tokens), "expires_in": 3600})
				return
			}
			requests++
			testutil.Equal(t, r.Header.Get("Authorization"), fmt.Sprintf("Bearer token-%d", tokens))
			switch requests {
			case 1:
				w.Header().Set("Retry-After", "90")
				http.Error(w, "rate limited", http.StatusTooManyRequests)
			case 2:
				testutil.Equal(t, time.Since(started), graphMaxRetryWait)
				http.Error(w, "token expired", http.StatusUnauthorized)
			default:
				w.WriteHeader(http.StatusAccepted)
			}
		}))
		endpoint := "http://example.com"
		client := &graphClient{
			fromAddress: "sender@example.com", tokenURL: endpoint + "/token",
			baseURL: endpoint, httpClient: server.Client(),
		}
		testutil.NoError(t, client.SendMail(t.Context(), []string{"ops@example.com"}, "subject", "body"))
		testutil.Equal(t, requests, 3)
		testutil.Equal(t, tokens, 2)
		testutil.Equal(t, time.Since(started), graphMaxRetryWait+2*time.Second)

		requests = 0
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := client.SendMail(ctx, []string{"ops@example.com"}, "subject", "body"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancel during backoff: %v", err)
		}
		testutil.Equal(t, requests, 1)
		testutil.Equal(t, time.Since(started), graphMaxRetryWait+3*time.Second)
	})
}

func TestRetryAfterDelay(t *testing.T) {
	for _, tt := range []struct {
		name, value string
		want        time.Duration
	}{
		{name: "valid", value: "10", want: 10 * time.Second},
		{name: "capped", value: "90", want: graphMaxRetryWait},
		{name: "duration overflow", value: "9223372036854775807", want: graphMaxRetryWait},
		{name: "zero", value: "0"},
		{name: "negative", value: "-1"},
		{name: "invalid", value: "invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testutil.Equal(t, retryAfterDelay(tt.value), tt.want)
		})
	}
}

func TestFormatMessageIncludesConditionKey(t *testing.T) {
	if !strings.Contains(formatMessage("ALERT", Event{Key: "key", Summary: "summary"}, time.Time{}).body, "Condition: key") {
		t.Error("formatted message omits condition key")
	}
}
