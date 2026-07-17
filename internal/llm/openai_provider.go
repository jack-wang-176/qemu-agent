package llm

import (
	"context"
	"errors"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

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
	return Capabilities{Tools: true, Streaming: false}
}

func (o *OpenAIProvider) Stream(context.Context, Request) (<-chan StreamEvent, error) {
	return nil, errors.New("streaming is not implemented")
}
