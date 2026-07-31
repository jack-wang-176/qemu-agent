package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
)

type fakeClient struct {
	mu      sync.Mutex
	polls   []PollResult
	pollErr error
	sent    []SendRequest
	edited  []EditRequest
	nextID  int64
}

func (f *fakeClient) Poll(ctx context.Context, _ PollRequest) (PollResult, error) {
	f.mu.Lock()
	if len(f.polls) > 0 {
		result := f.polls[0]
		f.polls = f.polls[1:]
		f.mu.Unlock()
		return result, nil
	}
	err := f.pollErr
	f.mu.Unlock()
	if err != nil {
		return PollResult{}, err
	}
	<-ctx.Done()
	return PollResult{}, ctx.Err()
}
func (f *fakeClient) SendMessage(_ context.Context, req SendRequest) (SentMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.sent = append(f.sent, req)
	return SentMessage{ID: f.nextID, ChatID: req.Target.ChatID, Text: req.Text}, nil
}
func (f *fakeClient) EditMessage(_ context.Context, req EditRequest) (SentMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edited = append(f.edited, req)
	return SentMessage{ID: req.MessageID, ChatID: req.ChatID, Text: req.Text}, nil
}

func TestIdentitySessionKey(t *testing.T) {
	tests := []struct {
		identity Identity
		want     string
	}{
		{Identity{UserID: 1, ChatID: 1, ChatType: "private"}, "telegram:user:1"},
		{Identity{UserID: 1, ChatID: -2, ChatType: "group"}, "telegram:chat:-2:user:1"},
		{Identity{UserID: 1, ChatID: -2, ThreadID: 3, ChatType: "supergroup"}, "telegram:chat:-2:thread:3:user:1"},
	}
	for _, test := range tests {
		if got := test.identity.SessionKey(); got != test.want {
			t.Fatalf("key=%q want=%q", got, test.want)
		}
	}
}

func TestRequestSinkStreamsChunksAndFinishes(t *testing.T) {
	client := &fakeClient{}
	factory, err := NewEventSinkFactory(client, SinkConfig{EditInterval: time.Hour, ChunkSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	sink, _ := factory.New(Target{ChatID: 10})
	ctx := context.Background()
	for _, event := range []runstream.Event{
		{Sequence: 1, Type: runstream.EventRunStarted},
		{Sequence: 2, Type: runstream.EventTextDelta, Text: "abc"},
		{Sequence: 3, Type: runstream.EventTextDelta, Text: "def"},
		{Sequence: 4, Type: runstream.EventRunCompleted},
	} {
		if err := sink.Emit(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sent) != 2 || client.sent[0].Text != "abc" || client.sent[1].Text != "ef" {
		t.Fatalf("sent=%#v", client.sent)
	}
	if len(client.edited) != 1 || client.edited[0].Text != "abcd" {
		t.Fatalf("edited=%#v", client.edited)
	}
	if !sink.StreamedText() {
		t.Fatal("StreamedText=false")
	}
	if !sink.Rendered() {
		t.Fatal("Rendered=false")
	}
}

type handlerFunc func(context.Context, channel.Request) (channel.Outbound, error)

func (f handlerFunc) Handle(ctx context.Context, req channel.Request) (channel.Outbound, error) {
	return f(ctx, req)
}

func TestChannelMapsAuthorizedUpdateAndUsesFallbackReply(t *testing.T) {
	client := &fakeClient{polls: []PollResult{{Updates: []Update{{ID: 1, Message: &Message{ID: 2, From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "hello"}}}, NextOffset: 2}}}
	factory, _ := NewEventSinkFactory(client, SinkConfig{EditInterval: time.Second, ChunkSize: 100})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport, err := New(Dependencies{Client: client, Events: factory, Logger: logger}, Config{AllowedUserIDs: []int64{100}, PollTimeout: time.Second, RetryMinBackoff: time.Millisecond, RetryMaxBackoff: time.Second, MaxConcurrency: 1, MaxInputBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	called := make(chan channel.Request, 1)
	done := make(chan error, 1)
	go func() {
		done <- transport.Run(ctx, handlerFunc(func(_ context.Context, req channel.Request) (channel.Outbound, error) {
			called <- req
			return channel.Outbound{SessionKey: req.Inbound.SessionKey, Text: "answer", Action: channel.ActionReply}, nil
		}))
	}()
	select {
	case req := <-called:
		if req.Inbound.SessionKey != "telegram:user:100" || req.Capabilities.InteractiveApproval {
			t.Fatalf("request=%#v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("handler not called")
	}
	deadline := time.Now().Add(time.Second)
	for {
		client.mu.Lock()
		sent := len(client.sent)
		client.mu.Unlock()
		if sent == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fallback reply not sent")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sent) != 1 || client.sent[0].Text != "answer" {
		t.Fatalf("sent=%#v", client.sent)
	}
}

func TestChannelRejectsUnauthorizedUpdate(t *testing.T) {
	client := &fakeClient{}
	factory, _ := NewEventSinkFactory(client, SinkConfig{EditInterval: time.Second, ChunkSize: 100})
	transport, _ := New(Dependencies{Client: client, Events: factory, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Config{AllowedUserIDs: []int64{100}, PollTimeout: time.Second, RetryMinBackoff: time.Millisecond, RetryMaxBackoff: time.Second, MaxConcurrency: 1, MaxInputBytes: 100})
	_, _, ok := transport.mapUpdate(Update{ID: 1, Message: &Message{ID: 2, From: &User{ID: 200}, Chat: Chat{ID: 200, Type: "private"}, Text: "hello"}})
	if ok {
		t.Fatal("unauthorized update accepted")
	}
}

func TestAPIErrorClassification(t *testing.T) {
	err := &APIError{Kind: ErrorRateLimited, RetryAfter: time.Second, Err: errors.New("x")}
	if !IsRetryable(err) || RetryAfter(err) != time.Second || IsAuthentication(err) {
		t.Fatal("classification failed")
	}
}
