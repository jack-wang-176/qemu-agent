package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/prompt"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

// Config contains Agent loop behavior only. Model selection belongs to Session.
type Config struct {
	MaxTurns int
	Stream   bool
	// MemoryTopK is how many memories one request may ask for. The assembler
	// clamps it again, so this is the caller-facing ceiling, not the only one.
	MemoryTopK int
	// PromptReservedTokens is the room kept free for the request-scoped overlay.
	// It is subtracted from the history budget, never added to MaxContext, so a
	// larger overlay compacts history instead of overflowing the model.
	PromptReservedTokens int
	// PromptMaxBytes bounds the rendered overlay. Zero defers to the assembler.
	PromptMaxBytes int
}

type ToolCatalog interface {
	Schemas() []llm.ToolSchema
}

type SecureToolExecutor interface {
	Execute(context.Context, security.Invocation) (security.Result, error)
}

type ContextManager interface {
	EnforceBudget(ctx context.Context, budget contextmgr.ModelBudget, msgs []llm.Message) ([]llm.Message, int, error)
}

// PromptAssembler produces the request-scoped view of the transcript. Prepare
// runs once per request (retrieval), Build runs once per turn (rendering); the
// split is what keeps a multi-turn run from re-ranking memories between turns.
type PromptAssembler interface {
	Prepare(context.Context, prompt.ContextQuery) (prompt.Snapshot, error)
	Build(context.Context, prompt.Input) (prompt.Plan, error)
}

// Dependencies contains runtime capabilities required by Agent.
type Dependencies struct {
	Models   llm.ModelResolver
	Catalog  ToolCatalog
	Executor SecureToolExecutor
	Store    session.Store
	Context  ContextManager
	Prompts  PromptAssembler
	Logger   *slog.Logger
	NewID    func() string
	Now      func() time.Time
}

type Agent struct {
	models         llm.ModelResolver
	catalog        ToolCatalog
	executor       SecureToolExecutor
	ctxmgr         ContextManager
	prompts        PromptAssembler
	store          session.Store
	maxTurns       int
	stream         bool
	memoryTopK     int
	reservedTokens int
	promptBytes    int
	logger         *slog.Logger
	newID          func() string
	now            func() time.Time
}

func New(deps Dependencies, cfg Config) (*Agent, error) {
	if deps.Models == nil {
		return nil, errors.New("model resolver is nil")
	}
	if deps.Catalog == nil {
		return nil, errors.New("tool catalog is nil")
	}
	if deps.Executor == nil {
		return nil, errors.New("tool executor is nil")
	}
	if deps.Store == nil {
		return nil, errors.New("session store is nil")
	}
	if deps.Context == nil {
		return nil, errors.New("context manager is nil")
	}
	// A disabled knowledge layer is expressed as prompt.NopAssembler, never as a
	// nil field: an optional dependency that has to be nil-checked on the hot
	// path is one forgotten check away from a panic mid-run.
	if deps.Prompts == nil {
		return nil, errors.New("prompt assembler is nil")
	}
	if deps.Logger == nil {
		return nil, errors.New("logger is nil")
	}
	if cfg.MaxTurns <= 0 {
		return nil, errors.New("max turns must be positive")
	}
	if cfg.MemoryTopK < 0 || cfg.PromptReservedTokens < 0 || cfg.PromptMaxBytes < 0 {
		return nil, errors.New("prompt limits must be >= 0")
	}
	if deps.NewID == nil {
		return nil, errors.New("invocation id generator is nil")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}

	return &Agent{
		models:         deps.Models,
		catalog:        deps.Catalog,
		executor:       deps.Executor,
		store:          deps.Store,
		ctxmgr:         deps.Context,
		prompts:        deps.Prompts,
		maxTurns:       cfg.MaxTurns,
		stream:         cfg.Stream,
		memoryTopK:     cfg.MemoryTopK,
		reservedTokens: cfg.PromptReservedTokens,
		promptBytes:    cfg.PromptMaxBytes,
		logger:         deps.Logger,
		newID:          deps.NewID,
		now:            deps.Now,
	}, nil
}

func (a *Agent) completeTurn(ctx context.Context, resolved llm.ResolvedModel, req llm.Request, emitter *emitter, turn int) (response *llm.Response, err error) {
	if !a.stream {
		resp, err := resolved.Provider.Complete(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("complete model turn: %w", err)
		}
		return validateResponse(resp)
	}
	if !resolved.Definition.Streaming {
		return nil, fmt.Errorf("model %q does not support streaming", resolved.Definition.Ref.String())
	}
	if !resolved.Provider.Capability().Streaming {
		return nil, fmt.Errorf("provider %q does not support streaming", resolved.Provider.Name())
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := resolved.Provider.Stream(streamCtx, req)
	if err != nil {
		return nil, fmt.Errorf("start model stream: %w", err)
	}
	if stream == nil {
		return nil, errors.New("provider returned nil stream")
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close model stream: %w", closeErr))
		}
	}()

	acc := llm.NewStreamAccumulator()
	for {
		event, recvErr := stream.Recv(streamCtx)
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) && acc.Done() {
				break
			}
			return nil, fmt.Errorf("receive model stream: %w", recvErr)
		}
		if err := acc.Apply(event); err != nil {
			return nil, fmt.Errorf("accumulate model stream: %w", err)
		}
		if event.TextDelta != "" {
			if err := emitter.Emit(streamCtx, runstream.Event{Type: runstream.EventTextDelta, Turn: turn, Text: event.TextDelta}); err != nil {
				cancel()
				return nil, fmt.Errorf("%w: %v", ErrEventDelivery, err)
			}
		}
		if event.Done {
			break
		}
	}
	response, err = acc.Finalize()
	if err != nil {
		return nil, fmt.Errorf("finalize model stream: %w", err)
	}
	return validateResponse(response)
}

func validateResponse(resp *llm.Response) (*llm.Response, error) {
	if resp == nil {
		return nil, errors.New("provider returned nil response")
	}
	if resp.Message.Role != llm.RoleAssistant {
		return nil, fmt.Errorf("provider response role is %q; want assistant", resp.Message.Role)
	}
	if len(resp.Message.ToolCalls) == 0 && resp.Message.Content == "" {
		return nil, errors.New("provider response has neither content nor tool calls")
	}
	seen := make(map[string]struct{}, len(resp.Message.ToolCalls))
	for index, toolcall := range resp.Message.ToolCalls {
		if toolcall.Args == "" || toolcall.Name == "" || toolcall.ID == "" {
			return nil, fmt.Errorf("provider tool call %d is incomplete", index)
		}
		if !json.Valid([]byte(toolcall.Args)) {
			return nil, fmt.Errorf("provider tool call %d arguments are not valid JSON", index)
		}
		if _, exist := seen[toolcall.ID]; exist {
			return nil, fmt.Errorf("provider returned duplicate tool call id %q", toolcall.ID)
		}
		seen[toolcall.ID] = struct{}{}
	}
	if resp.Usage.CompletionToken < 0 || resp.Usage.PromptToken < 0 || resp.Usage.TotalToken < 0 {
		return nil, errors.New("provider response usage contains negative token count")
	}
	return resp, nil
}

func publicError(err error) (kind, summary string) {
	switch {
	case err == nil:
		return "", ""
	case errors.Is(err, context.Canceled):
		return "canceled", "request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "request timed out"
	case errors.Is(err, security.ErrDenied):
		return "tool_denied", "tool invocation was denied"
	case errors.Is(err, security.ErrApprovalDeclined):
		return "approval_declined", "tool approval was declined"
	case errors.Is(err, ErrEventDelivery):
		return "event_delivery", "run event delivery failed"
	default:
		return "internal", "agent run failed"
	}
}

func toolResultText(name string, err error) string {
	kind, summary := publicError(err)
	return fmt.Sprintf("ERROR: tool %q failed (%s): %s", name, kind, summary)
}

func validateRunInput(input RunInput) error {
	if strings.TrimSpace(input.Text) == "" {
		return errors.New("input is empty")
	}
	if strings.TrimSpace(input.SessionKey) == "" {
		return errors.New("session key is empty")
	}
	if strings.TrimSpace(input.Channel) == "" {
		return errors.New("channel is empty")
	}
	return nil
}
