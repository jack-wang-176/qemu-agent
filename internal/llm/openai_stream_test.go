package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestOpenAIProviderStreamConvertsSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["stream"] != true {
			t.Fatalf("stream = %#v", body["stream"])
		}
		streamOptions, ok := body["stream_options"].(map[string]any)
		if !ok || streamOptions["include_usage"] != true {
			t.Fatalf("stream_options = %#v", body["stream_options"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_\",\"type\":\"function\",\"function\":{\"name\":\"ba\",\"arguments\":\"{\\\"com\"}}]},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := &OpenAIProvider{
		name: "test",
		client: openai.NewClient(
			option.WithAPIKey("test"),
			option.WithBaseURL(server.URL),
		),
	}
	stream, err := provider.Stream(context.Background(), Request{Model: "test", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	text, err := stream.Recv(context.Background())
	if err != nil || text.TextDelta != "hel" || text.Done {
		t.Fatalf("text event = %#v, err = %v", text, err)
	}
	tool, err := stream.Recv(context.Background())
	if err != nil || len(tool.ToolCallDeltas) != 1 || tool.ToolCallDeltas[0].ID != "call_" || tool.ToolCallDeltas[0].Name != "ba" {
		t.Fatalf("tool event = %#v, err = %v", tool, err)
	}
	done, err := stream.Recv(context.Background())
	if err != nil || !done.Done || done.Usage == nil || done.Usage.TotalToken != 5 {
		t.Fatalf("done event = %#v, err = %v", done, err)
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after done error = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}
