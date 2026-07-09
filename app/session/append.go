package session

import (
	"github.com/openai/openai-go/v3"
)

func (s *Session) AddChatResult(cpl *openai.ChatCompletion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var saParam = cpl.Choices[0].Message
	s.Msg = append(s.Msg, saParam.ToParam())
}
func (s *Session) AddToolResult(data, ID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Msg = append(s.Msg, openai.ToolMessage(data, ID))
}
