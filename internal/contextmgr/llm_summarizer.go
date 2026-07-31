package contextmgr

import (
	"context"

	"fmt"

	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type LLMSummarizer struct {
	Provider *llm.OpenAIProvider
	/* this mean keep recent turn.*/
	KeepRecent int
	Model      string
}

func NewLLMSummarizer(cli *llm.OpenAIProvider, keeprecent int, model string) *LLMSummarizer {
	return &LLMSummarizer{
		Provider:   cli,
		KeepRecent: keeprecent,
		Model:      model,
	}
}
func (l *LLMSummarizer) Name() string {
	return "LLMSummarizer"
}

/* compact old history to new history.*/
func (l *LLMSummarizer) Compact(ctx context.Context, model string, history session.History) (session.History, bool, error) {
	if l.KeepRecent < 0 {
		return history, false, fmt.Errorf("invalid keeprecent")
	}
	if len(history.Turns) <= (l.KeepRecent) {
		return history, false, nil
	}
	cut := len(history.Turns) - l.KeepRecent
	targetTurns := history.Turns[:cut]
	recentTurns := history.Turns[cut:]

	toBeCompact := RenderTurnsForSummary(targetTurns)
	/* build compact msg,system msg before imply this is msg after compact.*/
	resp, err := l.Provider.Complete(ctx, llm.Request{
		Model: l.Model,
		Messages: []llm.Message{
			{
				Role:    llm.RoleSystem,
				Content: "Summarize tool-agent history. Preserve decisions, files, commands, errors, and unfinished work.",
			},
			{
				Role:    llm.RoleUser,
				Content: toBeCompact,
			},
		},
	})

	if err != nil {
		return history, false, err
	}
	if resp == nil || strings.TrimSpace(resp.Message.Content) == "" {
		return history, false, fmt.Errorf("summarizer returned empty response")
	}

	result := session.History{
		Prefix:  append([]llm.Message(nil), history.Prefix...),
		Turns:   append([]session.Turn(nil), recentTurns...),
		Pending: append([]llm.Message(nil), history.Pending...),
	}
	result.Prefix = append(result.Prefix, llm.Message{
		Role:    llm.RoleSystem,
		Content: "【Compacted history summary】\n" + resp.Message.Content,
	})
	return result, true, nil
}

/* RenderTurnsForSummary this function turned turns into flat string which send to model*/
func RenderTurnsForSummary(turns []session.Turn) string {
	var builder strings.Builder
	for index, turn := range turns {
		fmt.Fprintf(&builder, "\n## Turn %d\n", index+1)
		for _, input := range turn.Input {
			fmt.Fprintf(&builder, "input(%s): %s\n", input.Role, input.Content)
		}
		if turn.Assistant.Content != "" {
			fmt.Fprintf(&builder, "assistant: %s\n", turn.Assistant.Content)
		}
		for _, call := range turn.Assistant.ToolCalls {
			fmt.Fprintf(&builder, "tool_call id=%s name=%s args=%s\n", call.ID, call.Name, call.Args)
		}
		for _, tool := range turn.Tools {
			fmt.Fprintf(&builder, "tool_result id=%s: %s\n", tool.ToolCallID, tool.Content)
		}
	}
	return builder.String()
}
