package llm

import (
	"context"

	"errors"
	"fmt"
	"time"

	"github.com/openai/openai-go/v3"
)

/* AgentCallWithRetry call agent with certain retry time break due to like api error
 * the complex param build is not supposed to be in this funciont, forward to param
 * directly use it. This function is not supposed to be invoke by outward, all use is
 * supposed to be in llm package.
 */
func AgentCallWithRetry(ctx context.Context, client openai.Client, newParam openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	maxRetry := 4
	baseDelay := time.Second
	var finalErr error
	for attempt := 0; attempt < maxRetry; attempt++ {
		resp, err := client.Chat.Completions.New(ctx, newParam)
		if err == nil {
			return resp, nil
		}
		finalErr = err
		var apiErr *openai.Error
		/* do not retry if err is the like*/
		if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 &&
			apiErr.StatusCode < 500 && apiErr.StatusCode != 429 {
			return nil, err
		}
		/* done or sleep.*/
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(baseDelay << attempt):
		}
	}
	return nil, fmt.Errorf("llm call failed after %d retries: %w", maxRetry, finalErr)
}
