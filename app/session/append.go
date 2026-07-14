package session

import (
	"time"

	"github.com/openai/openai-go/v3"
)

func (s *Session) AddChatResult(cpl *openai.ChatCompletion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var saParam = cpl.Choices[0].Message
	s.Msg = append(s.Msg, saParam.ToParam())
	s.UpdatedAt = time.Now()
}

func (s *Session) AddToolResult(data, ID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Msg = append(s.Msg, openai.ToolMessage(data, ID))
	s.UpdatedAt = time.Now()
}

func (s *Session) AddUser(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Msg = append(s.Msg, openai.UserMessage(text))
	s.UpdatedAt = time.Now()
}
