package agent

// emit.go binds the run loop to the request's event stream. Sequencing,
// identity stamping and payload validation live in runstream; pipeline stages
// emit into the same stream and must obey the same rules. What is
// left here is the agent's own precondition: an event stream is only meaningful
// once a session exists, because SessionID and TraceID come from it.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

var ErrEventDelivery = errors.New("run event delivery failed")

func newEmitter(input RunInput, s *session.Session, now func() time.Time) (runstream.Emitter, error) {
	if s == nil {
		return nil, errors.New("event session is nil")
	}
	if strings.TrimSpace(input.SessionKey) == "" || strings.TrimSpace(input.Channel) == "" {
		return nil, errors.New("event request identity is incomplete")
	}
	emitter, err := runstream.NewEmitter(runstream.EmitterOptions{
		Sink: input.Events,
		Identity: runstream.Event{
			TraceID: s.TraceID, SessionID: s.ID,
			SessionKey: input.SessionKey, Channel: input.Channel,
		},
		Now: now,
	})
	if err != nil {
		return nil, fmt.Errorf("create run emitter: %w", err)
	}
	return emitter, nil
}
