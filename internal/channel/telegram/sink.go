package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jack-wang-176/qemu-agent/internal/runstream"
)

type RequestSink interface {
	runstream.EventSink
	Finish(context.Context) error
	StreamedText() bool
	Rendered() bool
}

type SinkConfig struct {
	EditInterval time.Duration
	ChunkSize    int
}

type EventSinkFactory struct {
	client Client
	cfg    SinkConfig
	now    func() time.Time
}

func NewEventSinkFactory(client Client, cfg SinkConfig) (*EventSinkFactory, error) {
	if client == nil {
		return nil, errors.New("telegram sink client is nil")
	}
	if cfg.EditInterval <= 0 || cfg.ChunkSize <= 0 {
		return nil, errors.New("telegram sink config is invalid")
	}
	return &EventSinkFactory{client: client, cfg: cfg, now: time.Now}, nil
}

func (f *EventSinkFactory) New(target Target) (RequestSink, error) {
	if target.ChatID == 0 {
		return nil, errors.New("telegram target chat id is zero")
	}
	return &requestSink{client: f.client, target: target, cfg: f.cfg, now: f.now}, nil
}

type requestSink struct {
	mu                                                     sync.Mutex
	client                                                 Client
	target                                                 Target
	cfg                                                    SinkConfig
	now                                                    func() time.Time
	sequence                                               uint64
	started, terminal, finished, streamed, rendered, dirty bool
	buffer                                                 strings.Builder
	messages                                               []SentMessage
	lastFlush                                              time.Time
}

func (s *requestSink) Emit(ctx context.Context, event runstream.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return errors.New("telegram request sink is finished")
	}
	if event.Sequence == 0 || event.Sequence <= s.sequence {
		return fmt.Errorf("telegram event sequence %d is not increasing", event.Sequence)
	}
	s.sequence = event.Sequence
	switch event.Type {
	case runstream.EventRunStarted:
		if s.started {
			return errors.New("duplicate telegram run_started")
		}
		s.started = true
	case runstream.EventTurnStarted:
		if !s.started || s.terminal {
			return errors.New("telegram turn_started outside active run")
		}
	case runstream.EventTextDelta:
		if !s.started || s.terminal || event.Text == "" {
			return errors.New("invalid telegram text_delta")
		}
		s.buffer.WriteString(event.Text)
		s.streamed = true
		s.rendered = true
		s.dirty = true
	case runstream.EventToolStarted:
		if !s.started || s.terminal {
			return errors.New("telegram tool_started outside active run")
		}
		s.appendLine("[tool] " + event.ToolName + " requested")
	case runstream.EventToolCompleted:
		if !s.started || s.terminal {
			return errors.New("telegram tool_completed outside active run")
		}
		status := "completed"
		if event.ToolOK == nil || !*event.ToolOK {
			status = "failed"
		}
		s.appendLine("[tool] " + event.ToolName + " " + status)
	case runstream.EventRunCompleted:
		if !s.started || s.terminal {
			return errors.New("invalid telegram run_completed")
		}
		s.terminal = true
	case runstream.EventRunFailed:
		if !s.started || s.terminal {
			return errors.New("invalid telegram run_failed")
		}
		s.appendLine("[run] failed: " + event.Summary)
		s.terminal = true
	default:
		return fmt.Errorf("unsupported telegram event %q", event.Type)
	}
	if s.dirty && (!s.streamed || s.lastFlush.IsZero() || s.now().Sub(s.lastFlush) >= s.cfg.EditInterval) {
		return s.flushLocked(ctx)
	}
	return nil
}

func (s *requestSink) appendLine(text string) {
	if s.buffer.Len() > 0 && !strings.HasSuffix(s.buffer.String(), "\n") {
		s.buffer.WriteByte('\n')
	}
	s.buffer.WriteString(text)
	s.buffer.WriteByte('\n')
	s.rendered = true
	s.dirty = true
}

func (s *requestSink) Finish(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.flushLocked(ctx)
	if err == nil {
		s.finished = true
	}
	return err
}
func (s *requestSink) StreamedText() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.streamed }
func (s *requestSink) Rendered() bool     { s.mu.Lock(); defer s.mu.Unlock(); return s.rendered }

func (s *requestSink) flushLocked(ctx context.Context) error {
	if !s.dirty {
		return nil
	}
	parts := splitText(s.buffer.String(), s.cfg.ChunkSize)
	shared := min(len(s.messages), len(parts))
	for index := 0; index < shared; index++ {
		if s.messages[index].Text == parts[index] {
			continue
		}
		updated, err := s.client.EditMessage(ctx, EditRequest{
			ChatID: s.target.ChatID, MessageID: s.messages[index].ID, Text: parts[index],
		})
		if err != nil {
			return err
		}
		s.messages[index] = updated
	}
	for len(s.messages) < len(parts) {
		part := parts[len(s.messages)]
		msg, err := s.client.SendMessage(ctx, SendRequest{Target: s.target, Text: part})
		if err != nil {
			return err
		}
		s.messages = append(s.messages, msg)
	}
	s.dirty = false
	s.lastFlush = s.now()
	return nil
}

func splitText(text string, maxRunes int) []string {
	if text == "" || maxRunes <= 0 {
		return nil
	}
	runes := []rune(text)
	result := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
	for len(runes) > 0 {
		n := min(maxRunes, len(runes))
		cut := n
		if n < len(runes) {
			for i := n; i > n/2; i-- {
				if runes[i-1] == '\n' || runes[i-1] == ' ' {
					cut = i
					break
				}
			}
		}
		part := string(runes[:cut])
		if utf8.ValidString(part) {
			result = append(result, part)
		}
		runes = runes[cut:]
	}
	return result
}
