package llm

import (
	"context"
	"errors"
	"fmt"
	"io"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

var ErrStreamClosed = errors.New("LLM stream is closed")

type openAIStream struct {
	stream *ssestream.Stream[openai.ChatCompletionChunk]

	finishSeen bool
	done       bool
	closed     bool
	closeErr   error
}

/* wrap clinet with model name,the name supposed to be in env.*/
type OpenAIProvider struct {
	client openai.Client
	name   string
}

func NewOpenAIProvider(name, apikey, baseurl string) *OpenAIProvider {
	return &OpenAIProvider{
		name:   name,
		client: openai.NewClient(option.WithAPIKey(apikey), option.WithBaseURL(baseurl)),
	}
}

func (o *OpenAIProvider) Name() string {
	return o.name
}

func (o *OpenAIProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	resp, err := AgentCallWithRetry(ctx, o.client, toOpenAIParams(req))
	if err != nil {
		return nil, err
	}
	return fromOpenAIResponse(resp), nil
}

func (o *OpenAIProvider) Capability() Capabilities {
	return Capabilities{Tools: true, Streaming: true}
}

func (o *OpenAIProvider) Stream(ctx context.Context, req Request) (Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o == nil {
		return nil, errors.New("OpenAI provider is nil")
	}
	if req.Model == "" {
		return nil, errors.New("OpenAI stream model is empty")
	}

	sdkStream := o.client.Chat.Completions.NewStreaming(ctx, convertStreamParams(req))
	if err := sdkStream.Err(); err != nil {
		_ = sdkStream.Close()
		return nil, fmt.Errorf("start OpenAI stream: %w", err)
	}
	return &openAIStream{stream: sdkStream}, nil
}

func (s *openAIStream) Recv(ctx context.Context) (StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return StreamEvent{}, err
	}
	if s == nil || s.stream == nil {
		return StreamEvent{}, errors.New("OpenAI stream is nil")
	}
	if s.closed {
		return StreamEvent{}, ErrStreamClosed
	}
	if s.done {
		return StreamEvent{}, io.EOF
	}

	for s.stream.Next() {
		event, err := fromOpenAIChunk(s.stream.Current())
		if err != nil {
			return StreamEvent{}, fmt.Errorf("convert OpenAI stream chunk: %w", err)
		}

		if event.Done {
			s.finishSeen = true
			event.Done = false
		}
		if s.finishSeen && event.Usage != nil {
			event.Done = true
			s.done = true
		}
		if hasStreamEventData(event) {
			return event, nil
		}
	}

	if err := s.stream.Err(); err != nil {
		return StreamEvent{}, fmt.Errorf("receive OpenAI stream: %w", err)
	}
	if !s.finishSeen {
		return StreamEvent{}, errors.New("OpenAI stream ended before a finish reason")
	}

	s.done = true
	return StreamEvent{Done: true}, nil
}

func (s *openAIStream) Close() error {
	if s == nil || s.closed {
		if s == nil {
			return nil
		}
		return s.closeErr
	}
	s.closed = true
	if s.stream != nil {
		s.closeErr = s.stream.Close()
	}
	return s.closeErr
}

func hasStreamEventData(event StreamEvent) bool {
	return event.TextDelta != "" || len(event.ToolCallDeltas) > 0 || event.Usage != nil || event.Done
}
