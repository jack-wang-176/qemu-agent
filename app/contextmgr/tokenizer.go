package contextmgr

import (
	"encoding/json"

	"github.com/openai/openai-go/v3"
	"github.com/pkoukk/tiktoken-go"
)

type Tokenizer struct {
	tkm *tiktoken.Tiktoken
}

func NewTokenizer(model string) (*Tokenizer, error) {
	tkm, err := tiktoken.EncodingForModel(model)
	if err != nil {
		tkm, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			return nil, err
		}
	}
	return &Tokenizer{tkm: tkm}, nil
}

func (t *Tokenizer) CountText(text string) int {
	return len(t.tkm.Encode(text, nil, nil))
}

func (t *Tokenizer) Count(msgs []openai.ChatCompletionMessageParamUnion) int {
	numToken := 0
	tokenPerMsg := 3
	for _, msg := range msgs {
		numToken += tokenPerMsg
		b, _ := json.Marshal(msg)
		numToken += t.CountText(string(b))
	}
	numToken += 3
	return numToken
}
