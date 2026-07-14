package pkg

import (
	"context"

	"errors"
	"fmt"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

func AgentCallWithRetry(ctx context.Context, client openai.Client, model string, msgs []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) (*openai.ChatCompletion, error) {
	maxRetry := 4
	baseDelay := time.Second
	var finalErr error
	for attempt := 0; attempt < maxRetry; attempt++ {
		resp, err := client.Chat.Completions.New(ctx,
			openai.ChatCompletionNewParams{
				Model:    shared.ChatModel(model),
				Messages: msgs,
				Tools:    tools,
			})
		if err == nil {
			return resp, nil
		}
		finalErr = err
		var apiErr *openai.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 &&
			apiErr.StatusCode < 500 && apiErr.StatusCode != 429 {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(baseDelay << attempt):
		}
	}
	return nil, fmt.Errorf("llm call failed after %d retries: %w", maxRetry, finalErr)
}
