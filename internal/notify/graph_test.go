package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
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
	requests, tokens := 0, 0
	server := testutil.Server(t, func(w http.ResponseWriter, r *http.Request) {
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
			http.Error(w, "token expired", http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	})

	var waits []time.Duration
	client := &graphClient{
		fromAddress: "sender@example.com", tokenURL: server.URL + "/token",
		baseURL: server.URL, httpClient: server.Client(),
		wait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		},
	}
	testutil.NoError(t, client.SendMail(t.Context(), []string{"ops@example.com"}, "subject", "body"))
	testutil.Equal(t, requests, 3)
	testutil.Equal(t, tokens, 2)
	if len(waits) != 2 || waits[0] != graphMaxRetryWait || waits[1] != 2*time.Second {
		t.Errorf("waits = %v, want rate-limit cap followed by exponential backoff", waits)
	}
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
