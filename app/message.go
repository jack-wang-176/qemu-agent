package main

import (
	"github.com/openai/openai-go/v3"
)

type message struct {
	msg []openai.ChatCompletionMessageParamUnion
}

func NewMessage() *message {
	return &message{
		msg: make([]openai.ChatCompletionMessageParamUnion, 0),
	}
}

type OpenaiMessage interface {
	AddMessage(msg openai.ChatCompletionMessageParamUnion)
}

func (m *message) AddMessage(msg openai.ChatCompletionMessageParamUnion) {

}
