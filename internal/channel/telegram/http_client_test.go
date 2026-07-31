package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPClientPollMapsUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			t.Fatalf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":[{"update_id":7,"message":{"message_id":8,"message_thread_id":9,"text":"hello","from":{"id":10,"is_bot":false,"username":"u"},"chat":{"id":11,"type":"private"}}}]}`)
	}))
	defer server.Close()
	client, err := NewHTTPClient("secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL
	result, err := client.Poll(context.Background(), PollRequest{Timeout: time.Second, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.NextOffset != 8 || len(result.Updates) != 1 {
		t.Fatalf("result=%#v", result)
	}
	message := result.Updates[0].Message
	if message == nil || message.From == nil || message.From.ID != 10 || message.Chat.ID != 11 || message.ThreadID != 9 {
		t.Fatalf("message=%#v", message)
	}
}

func TestHTTPClientClassifiesRateLimitWithoutLeakingToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"too many","parameters":{"retry_after":2}}`)
	}))
	defer server.Close()
	client, _ := NewHTTPClient("super-secret-token", server.Client())
	client.baseURL = server.URL
	_, err := client.SendMessage(context.Background(), SendRequest{Target: Target{ChatID: 1}, Text: "x"})
	if !IsRetryable(err) || RetryAfter(err) != 2*time.Second {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Fatal("error leaked token")
	}
}
