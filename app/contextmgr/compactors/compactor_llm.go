package compactors

import (
	"context"
	"encoding/json"

	"strings"

	"github.com/jack-wang-176/qemu-agent/app/pkg"
	"github.com/openai/openai-go/v3"
)

type LLMSummarizer struct {
	Client     *openai.Client
	KeepRecent int
	Model      string
}

func NewLLMSummarizer(cli *openai.Client, keeprecent int, model string) *LLMSummarizer {
	return &LLMSummarizer{
		Client:     cli,
		KeepRecent: keeprecent,
		Model:      model,
	}
}
func (l *LLMSummarizer) Name() string {
	return "LLMSummarizer"
}
func (l *LLMSummarizer) Compact(ctx context.Context, model string, msgs []openai.ChatCompletionMessageParamUnion) ([]openai.ChatCompletionMessageParamUnion, bool, error) {
	if len(msgs) < (l.KeepRecent + 1) {
		return msgs, false, nil
	}
	systemMsg := msgs[0]
	targetMsg := msgs[1 : len(msgs)-l.KeepRecent]
	recentMsg := msgs[len(msgs)-l.KeepRecent:]

	var builder strings.Builder
	builder.WriteString("请将以下对话历史总结为一段极其精简的状态报告，重点保留核心发现、报错信息、以及已执行的关键命令：\n\n")
	for _, m := range targetMsg {
		b, _ := json.Marshal(m)
		builder.WriteString(string(b))
		builder.WriteString("\n")
	}
	resp, err := pkg.AgentCallWithRetry(ctx, *l.Client, model, []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(builder.String()),
	}, nil)

	if err != nil {
		return msgs, false, err
	}
	summaryText := resp.Choices[0].Message.Content

	newMsg := make([]openai.ChatCompletionMessageParamUnion, 0, len(recentMsg)+2)
	newMsg = append(newMsg, systemMsg)
	newMsg = append(newMsg, openai.SystemMessage("【已折叠的历史上下文摘要】\n"+summaryText))
	newMsg = append(newMsg, recentMsg...)
	return newMsg, true, nil
}
