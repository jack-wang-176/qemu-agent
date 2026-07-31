package tool

import (
	openai "github.com/openai/openai-go/v3"
)

type Tool interface {
	Name() string
	Build() openai.ChatCompletionToolUnionParam
	Execute(args string) (string, error)
}
