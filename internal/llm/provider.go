package llm

import (
	"context"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

/* tool call record for assistant*/
type ToolCall struct {
	ID   string
	Name string
	Args string
}

/* tool info for invoke*/
type ToolSchema struct {
	Name        string
	Description string
	Parameters  map[string]any
}

/* this struct corresponding to useful,openai struct.*/
type Usage struct{ PromptToken, CompletionToken, TotalToken int64 }

/* a single message need to deliver to model.*/
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   //only for assistant
	ToolCallID string     `json:"tool_call_id,omitempty"` //map to tool
}

/* a certain request struct to build corresponding openai struct.*/
type Request struct {
	Model     string       `json:"model"`
	Messages  []Message    `json:"messages"`
	Tools     []ToolSchema `json:"tools,omitempty"`
	MaxTokens int          `json:"max_tokens,omitempty"`
	Stream    bool         `json:"stream,omitempty"`
}

/* Response is deliver message from model and translate.*/
type Response struct {
	Message Message
	Usage   Usage
}

/* initilize the stream function*/
type StreamEvent struct {
	DeltaText string
	ToolCall  *ToolCall
	Done      bool
	Err       error
}

type Capabilities struct {
	Tools      bool
	Streaming  bool
	MaxContext int
}

type Provider interface {
	Name() string
	Capability() Capabilities
	Complete(ctx context.Context, req Request) (*Response, error)
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}
