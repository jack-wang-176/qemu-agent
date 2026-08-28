package modelingagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	"github.com/jack-wang-176/qemu-agent/internal/modelingworkflow"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

const maxPersistentReplyBytes = 16 * 1024

type Dependencies struct {
	Workflow   modelingworkflow.Service
	Store      session.Store
	Context    ContextManager
	Logger     *slog.Logger
	NewID      func() string
	Now        func() time.Time
	Model      llm.ModelRef
	MaxContext int
}

type Runner struct {
	workflow   modelingworkflow.Service
	store      session.Store
	now        func() time.Time
	model      llm.ModelRef
	manager    ContextManager
	logger     *slog.Logger
	newID      func() string
	maxContext int
}

func NewRunner(deps Dependencies) Runner {
	return Runner{
		workflow:   deps.Workflow,
		store:      deps.Store,
		now:        deps.Now,
		manager:    deps.Context,
		model:      deps.Model,
		logger:     deps.Logger,
		newID:      deps.NewID,
		maxContext: deps.MaxContext,
	}
}

func (r *Runner) Run(
	ctx context.Context,
	live *session.Session,
	in agent.RunInput,
) (answer string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := r.validateRun(live, in); err != nil {
		return "", err
	}

	emitter, err := runstream.NewEmitter(runstream.EmitterOptions{
		Now:  r.now,
		Sink: in.Events,
		Identity: runstream.Event{
			TraceID:    live.TraceID,
			SessionID:  live.ID,
			SessionKey: in.SessionKey,
			Channel:    in.Channel,
		},
	})
	if err != nil {
		return "", fmt.Errorf("modelingagent: create run emitter: %w", err)
	}
	if err := emitter.Emit(ctx, runstream.Event{Type: runstream.EventRunStarted}); err != nil {
		return "", fmt.Errorf("%w: %v", ErrEventDelivery, err)
	}
	fail := func(cause error) error {
		return failModelingRun(ctx, emitter, cause)
	}

	working := live.Clone()
	priorMessageCount := len(working.Messages)
	working.AddUser(in.Text)
	compacted, usage, err := r.manager.EnforceBudget(ctx, contextmgr.ModelBudget{
		Ref:        r.model,
		MaxContext: r.maxContext,
	}, working.MessageCopy())
	if err != nil {
		return "", fail(fmt.Errorf("modelingagent: enforce session history budget: %w", err))
	}
	if err := validateCurrentInputBoundary(compacted, in.Text); err != nil {
		return "", fail(err)
	}
	working.MessageReplace(compacted, usage)

	history, err := projectConversationHistory(compacted[:len(compacted)-1])
	if err != nil {
		return "", fail(err)
	}
	if len(history) > maxInterpretHistory {
		history = append([]modelingworkflow.ConversationMsg(nil), history[len(history)-maxInterpretHistory:]...)
	}

	requestID := strings.TrimSpace(r.newID())
	if requestID == "" {
		return "", fail(errors.New("modelingagent: generated request id is empty"))
	}
	call := modelingworkflow.CallContext{
		RequestID:      requestID,
		TraceID:        live.TraceID,
		WorkspaceID:    in.WorkspaceID,
		UserID:         in.UserID,
		SessionID:      live.ID,
		SessionKey:     in.SessionKey,
		Channel:        in.Channel,
		IdempotencyKey: turnIdempotencyKey(live.ID, priorMessageCount, in.Text),
		Interactive:    in.Interactive,
	}
	bridge := NewEventBridge(emitter, r.logger)
	workflowCtx := pipelineapi.WithEventPublisher(ctx, bridge)
	workflowCtx = security.WithCaller(workflowCtx, security.Caller{
		TraceID: live.TraceID, SessionID: live.ID, SessionKey: in.SessionKey,
		Channel: in.Channel, Interactive: in.Interactive,
	})
	result, err := r.workflow.Handle(workflowCtx, call, modelingworkflow.Request{
		History:    history,
		Text:       in.Text,
		Hashistory: len(history) > 0,
	})
	if err != nil {
		return "", fail(err)
	}
	if err := validateWorkflowResult(result); err != nil {
		return "", fail(err)
	}
	working.AddAssistant(llm.Message{Role: llm.RoleAssistant, Content: result.Reply})

	if err := live.CanReplaceFrom(working); err != nil {
		return "", fail(fmt.Errorf("modelingagent: validate session commit: %w", err))
	}
	if err := r.store.Save(ctx, working); err != nil {
		return "", fail(fmt.Errorf("modelingagent: save completed run: %w", err))
	}
	if err := live.ReplaceFrom(working); err != nil {
		return "", fail(fmt.Errorf("modelingagent: commit completed run: %w", err))
	}
	if err := emitter.Emit(ctx, runstream.Event{Type: runstream.EventRunCompleted}); err != nil {
		return "", fmt.Errorf("%w after session commit: %v", ErrEventDelivery, err)
	}
	return result.Reply, nil
}

func (r *Runner) validateRun(s *session.Session, in agent.RunInput) error {
	switch {
	case r == nil:
		return errors.New("modelingagent: runner is nil")
	case r.workflow == nil:
		return errors.New("modelingagent: workflow is nil")
	case r.store == nil:
		return errors.New("modelingagent: session store is nil")
	case r.manager == nil:
		return errors.New("modelingagent: context manager is nil")
	case r.logger == nil:
		return errors.New("modelingagent: logger is nil")
	case r.newID == nil:
		return errors.New("modelingagent: id generator is nil")
	case r.maxContext <= 0:
		return errors.New("modelingagent: max context must be positive")
	case s == nil:
		return errors.New("modelingagent: session is nil")
	case strings.TrimSpace(s.ID) == "":
		return errors.New("modelingagent: session id is empty")
	case strings.TrimSpace(s.TraceID) == "":
		return errors.New("modelingagent: session trace id is empty")
	case strings.TrimSpace(in.Text) == "":
		return errors.New("modelingagent: input is empty")
	case strings.TrimSpace(in.SessionKey) == "":
		return errors.New("modelingagent: session key is empty")
	case strings.TrimSpace(in.Channel) == "":
		return errors.New("modelingagent: channel is empty")
	}
	if _, err := llm.NormalizeModelRef(r.model); err != nil {
		return fmt.Errorf("modelingagent: invalid runner model: %w", err)
	}
	return nil
}

func validateCurrentInputBoundary(messages []llm.Message, text string) error {
	if len(messages) == 0 {
		return errors.New("modelingagent: compaction removed the current input")
	}
	current := messages[len(messages)-1]
	if current.Role != llm.RoleUser || current.Content != text || len(current.ToolCalls) != 0 || current.ToolCallID != "" {
		return errors.New("modelingagent: compaction changed the current input boundary")
	}
	return nil
}

func projectConversationHistory(messages []llm.Message) ([]modelingworkflow.ConversationMsg, error) {
	history := make([]modelingworkflow.ConversationMsg, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case llm.RoleSystem, llm.RoleTool:
			continue
		case llm.RoleAssistant:
			if len(message.ToolCalls) != 0 || strings.TrimSpace(message.Content) == "" {
				continue
			}
			history = append(history, modelingworkflow.ConversationMsg{Role: "assistant", Text: message.Content})
		case llm.RoleUser:
			if strings.TrimSpace(message.Content) == "" {
				continue
			}
			history = append(history, modelingworkflow.ConversationMsg{Role: "user", Text: message.Content})
		default:
			return nil, fmt.Errorf("modelingagent: unsupported session message role %q", message.Role)
		}
	}
	return history, nil
}

func turnIdempotencyKey(sessionID string, priorMessages int, input string) string {
	inputDigest := sha256.Sum256([]byte(input))
	payload, _ := json.Marshal(struct {
		Version       int    `json:"version"`
		SessionID     string `json:"session_id"`
		PriorMessages int    `json:"prior_messages"`
		InputDigest   string `json:"input_digest"`
	}{
		Version:       1,
		SessionID:     sessionID,
		PriorMessages: priorMessages,
		InputDigest:   hex.EncodeToString(inputDigest[:]),
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func validateWorkflowResult(result modelingworkflow.Result) error {
	if strings.TrimSpace(result.Reply) == "" {
		return errors.New("modelingagent: workflow returned an empty reply")
	}
	if len(result.Reply) > maxPersistentReplyBytes {
		return errors.New("modelingagent: workflow reply exceeds the session limit")
	}
	switch result.State {
	case modelingworkflow.StateNeedsInput, modelingworkflow.StateFailed:
	case modelingworkflow.StateWorking, modelingworkflow.StateAwaitingApply, modelingworkflow.StateCompleted:
		if result.Project == nil {
			return errors.New("modelingagent: workflow result is missing its project")
		}
	default:
		return fmt.Errorf("modelingagent: workflow returned invalid state %q", result.State)
	}
	if result.Project != nil {
		if err := modelingapi.ValidateProjectView(*result.Project); err != nil {
			return fmt.Errorf("modelingagent: workflow returned an invalid project: %w", err)
		}
	}
	for _, artifact := range result.Artifact {
		if err := modelingapi.ValidateArtifactDescriptor(artifact); err != nil {
			return fmt.Errorf("modelingagent: workflow returned an invalid artifact: %w", err)
		}
	}
	for _, evidence := range result.Evidence {
		if err := modelingapi.ValidateArtifactDescriptor(evidence); err != nil {
			return fmt.Errorf("modelingagent: workflow returned invalid evidence: %w", err)
		}
		if evidence.Kind != "evidence" {
			return errors.New("modelingagent: workflow evidence has a non-evidence kind")
		}
	}
	return nil
}

func failModelingRun(ctx context.Context, emitter runstream.Emitter, cause error) error {
	kind, summary := publicWorkflowError(cause)
	emitErr := emitter.Emit(ctx, runstream.Event{
		Type: runstream.EventRunFailed, ErrorKind: kind, Summary: summary,
	})
	if emitErr != nil {
		emitErr = fmt.Errorf("%w: %v", ErrEventDelivery, emitErr)
	}
	return errors.Join(cause, emitErr)
}

func publicWorkflowError(err error) (kind, summary string) {
	if errors.Is(err, context.Canceled) {
		return "canceled", "The modeling request was canceled."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "unavailable", "The modeling request timed out."
	}
	var workflowErr *modelingworkflow.Error
	if errors.As(err, &workflowErr) {
		return string(workflowErr.Kind), workflowFailureSummary(workflowErr.Kind)
	}
	if errors.Is(err, ErrEventDelivery) {
		return "event_delivery", "Modeling event delivery failed."
	}
	return "internal", "The modeling agent could not complete the request."
}

func workflowFailureSummary(kind modelingworkflow.ErrorKind) string {
	switch kind {
	case modelingworkflow.ErrorInvalidInput:
		return "The modeling request is invalid."
	case modelingworkflow.ErrorConflict:
		return "The modeling project changed before the request completed."
	case modelingworkflow.ErrorUnavailable:
		return "The modeling capability is unavailable."
	case modelingworkflow.ErrorNotFound:
		return "The modeling project was not found."
	case modelingworkflow.ErrorDenied:
		return "A required modeling action was denied."
	case modelingworkflow.ErrorApprovalRequired:
		return "The modeling request requires approval."
	case modelingworkflow.ErrorApprovalDeclined:
		return "The modeling approval was declined."
	case modelingworkflow.ErrorCanceled:
		return "The modeling request was canceled."
	default:
		return "The modeling agent could not complete the request."
	}
}
