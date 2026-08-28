package modelingworkflow

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

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

type Dependencies struct {
	Modeling    modelingapi.Service
	Binding     Store
	Interpreter Interpreter
	Presenter   Presenter
	NewID       func() string
	Now         func() time.Time
	Logger      *slog.Logger
}

type Config struct {
	MaxOpertionPerTurn int
	ArtifactReadLimit  int
}

const defaultArtifactReadLimit = modelingapi.MaxPageSize

type Controller struct {
	modeling          modelingapi.Service
	binding           Store
	Interpreter       Interpreter
	Presenter         Presenter
	maxOperations     int
	artifactReadLimit int
	now               func() time.Time
}

var _ Service = (*Controller)(nil)

func (c *Controller) Handle(
	ctx context.Context,
	call CallContext,
	req Request,
) (result Result, err error) {
	defer func() {
		if err != nil {
			err = mapModelingError(err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := validateCallContext(call); err != nil {
		return Result{}, err
	}
	if err := validateRequest(req); err != nil {
		return Result{}, err
	}

	apiCall := flowContextToModelingAPI(call)
	capabilities, err := c.modeling.Capabilities(ctx, apiCall)
	if err != nil {
		return Result{}, err
	}
	if err := modelingapi.ValidateCapabilities(capabilities); err != nil {
		return Result{}, fmt.Errorf("modelingworkflow: invalid capabilities: %w", err)
	}

	key := generateBindingKey(call)
	binding, found, err := c.binding.Load(ctx, key)
	if err != nil {
		return Result{}, err
	}

	intent, err := c.Interpreter.Interpret(ctx, InterpretInput{
		History:     req.History,
		Text:        req.Text,
		HasHistory:  req.Hashistory,
		Awaiting:    binding.Awaiting,
		WorkspaceID: call.WorkspaceID,
		UserID:      call.UserID,
	})
	if err != nil {
		return Result{}, err
	}

	var project modelingapi.ProjectView
	hasProject := found && strings.TrimSpace(string(binding.ActiveProjectID)) != ""
	// Starting a new project must remain possible even when the old binding points
	// to a project that can no longer be inspected.
	if hasProject && intent.Kind != IntentStartNew {
		project, err = c.modeling.Show(ctx, apiCall, modelingapi.ShowRequest{
			ProjectID: binding.ActiveProjectID,
		})
		if err != nil {
			return Result{}, err
		}
		if project.ID != binding.ActiveProjectID {
			return Result{}, errors.New("modelingworkflow: binding and project identity differ")
		}
		project = modelingapi.CloneProjectView(project)
	}

	var presentation Presentation
	switch intent.Kind {
	case IntentStart:
		if hasProject {
			presentation = projectPresentation(
				StateNeedsInput,
				"An active modeling project already exists.",
				"Do you want to continue the current project or start a new one?",
				project,
			)
			break
		}
		presentation, err = c.start(ctx, call, key, binding, found, false, capabilities, intent)
		if err != nil {
			return Result{}, err
		}

	case IntentStartNew:
		presentation, err = c.start(ctx, call, key, binding, found, true, capabilities, intent)
		if err != nil {
			return Result{}, err
		}

	case IntentInspect:
		if !hasProject {
			presentation = missingProjectPresentation()
			break
		}
		presentation = readOnlyProjectPresentation(project, "The active modeling project was inspected.")

	case IntentContinue:
		if !hasProject {
			presentation = missingProjectPresentation()
			break
		}
		var blocked *Presentation
		blocked, err = c.continueGuard(ctx, call, binding, project, capabilities)
		if err != nil {
			return Result{}, err
		}
		if blocked != nil {
			presentation = *blocked
			break
		}
		presentation, err = c.advanceUntilBoundary(ctx, call, binding, project, capabilities)
		if err != nil {
			return Result{}, err
		}

	case IntentProvideInput:
		presentation, err = c.provideInput(ctx, call, key, binding, found, project, hasProject, capabilities, intent)
		if err != nil {
			return Result{}, err
		}

	case IntentReadArtifact:
		if !hasProject {
			presentation = missingProjectPresentation()
			break
		}
		presentation, err = c.readArtifact(ctx, apiCall, project, intent.ArtifactID)
		if err != nil {
			return Result{}, err
		}

	case IntentEvidence:
		if !hasProject {
			presentation = missingProjectPresentation()
			break
		}
		presentation, err = c.readEvidence(ctx, apiCall, project)
		if err != nil {
			return Result{}, err
		}

	default:
		return Result{}, fmt.Errorf("modelingworkflow: unsupported intent %q", intent.Kind)
	}
	return c.resultFromPresentation(ctx, presentation)
}

func (c *Controller) readEvidence(
	ctx context.Context,
	call modelingapi.CallContext,
	project modelingapi.ProjectView,
) (Presentation, error) {
	page, err := c.modeling.Evidence(ctx, call, modelingapi.EvidenceRequest{
		ProjectID: project.ID,
		Limit:     modelingapi.MaxPageSize,
	})
	if err != nil {
		return Presentation{}, err
	}
	if err := modelingapi.ValidateEvidencePage(page); err != nil {
		return Presentation{}, fmt.Errorf("modelingworkflow: invalid Evidence result: %w", err)
	}
	summary := "No verification evidence is available for this project."
	if len(page.Evidence) > 0 {
		summary = "Verification evidence descriptors are available for review."
	}
	presentation := readOnlyProjectPresentation(project, summary)
	presentation.Evidence = cloneArtifactDescriptors(page.Evidence)
	return presentation, nil
}

func (c *Controller) continueGuard(
	ctx context.Context,
	call CallContext,
	binding Binding,
	project modelingapi.ProjectView,
	capabilities modelingapi.Capabilities,
) (*Presentation, error) {
	switch project.Status {
	case modelingapi.ProjectCompleted:
		presentation := projectPresentation(
			StateCompleted,
			"The active modeling project is complete.",
			"",
			project,
		)
		return &presentation, nil
	case modelingapi.ProjectBlocked:
		if project.BlockedReason == "awaiting_apply" {
			presentation, err := awaitingApplyPresentation(c, ctx, call, project, project.Artifacts, nil)
			if err != nil {
				return nil, err
			}
			return &presentation, nil
		}
		state := StateNeedsInput
		question := "What additional information should be provided?"
		if project.PublicError != nil {
			state = StateFailed
			question = ""
		}
		presentation := projectPresentation(state, "The active modeling project is blocked.", question, project)
		if project.PublicError != nil {
			failure := modelingapi.ClonePublicError(*project.PublicError)
			presentation.Failure = &failure
		}
		return &presentation, nil
	}

	operation, ok := findOperation(capabilities, project.CurrentOperation)
	if !ok {
		return nil, fmt.Errorf("modelingworkflow: engine does not expose operation %q", project.CurrentOperation)
	}
	if !operation.RequiresSources || len(binding.Sources) > 0 {
		return nil, nil
	}

	updated := cloneBinding(binding)
	updated.Awaiting = AwaitingSource
	updated.UpdatedAt = c.currentTime()
	if _, err := c.binding.CompareAndSave(ctx, updated, binding.Version); err != nil {
		return nil, err
	}
	presentation := projectPresentation(
		StateNeedsInput,
		"The next modeling operation requires source material.",
		"Which workspace source should be used?",
		project,
	)
	return &presentation, nil
}

func (c *Controller) provideInput(
	ctx context.Context,
	call CallContext,
	key BindingKey,
	binding Binding,
	found bool,
	project modelingapi.ProjectView,
	hasProject bool,
	capabilities modelingapi.Capabilities,
	intent Intent,
) (Presentation, error) {
	if !hasProject {
		startIntent := intent
		startIntent.Kind = IntentStart
		return c.start(ctx, call, key, binding, found, false, capabilities, startIntent)
	}

	updated := cloneBinding(binding)
	switch binding.Awaiting {
	case AwaitingRequirement:
		if intent.Title == "" && intent.Instruction == "" {
			return Presentation{}, errors.New("modelingworkflow: requirement input is empty")
		}
		if intent.Title != "" {
			if err := modelingapi.ValidateTitle(intent.Title); err != nil {
				return Presentation{}, err
			}
			updated.Title = strings.TrimSpace(intent.Title)
		}
		if err := modelingapi.ValidateInstruction(intent.Instruction); err != nil {
			return Presentation{}, err
		}
		if intent.Instruction != "" {
			updated.Instruction = strings.TrimSpace(intent.Instruction)
		}
	case AwaitingSource:
		if err := modelingapi.ValidateSources(intent.Sources); err != nil {
			return Presentation{}, err
		}
		if len(intent.Sources) == 0 {
			return Presentation{}, errors.New("modelingworkflow: source input is empty")
		}
		updated.Sources = modelingapi.CloneSources(intent.Sources)
	case AwaitingNone:
		return Presentation{}, errors.New("modelingworkflow: no input is currently awaited")
	default:
		return Presentation{}, fmt.Errorf("modelingworkflow: unknown awaiting state %q", binding.Awaiting)
	}

	updated.Awaiting = AwaitingNone
	updated.UpdatedAt = c.currentTime()
	saved, err := c.binding.CompareAndSave(ctx, updated, binding.Version)
	if err != nil {
		return Presentation{}, err
	}
	blocked, err := c.continueGuard(ctx, call, saved, project, capabilities)
	if err != nil {
		return Presentation{}, err
	}
	if blocked != nil {
		return *blocked, nil
	}
	return c.advanceUntilBoundary(ctx, call, saved, project, capabilities)
}

func (c *Controller) readArtifact(
	ctx context.Context,
	call modelingapi.CallContext,
	project modelingapi.ProjectView,
	artifactID modelingapi.ArtifactID,
) (Presentation, error) {
	descriptor, ok := findProjectArtifact(project, artifactID)
	if !ok {
		return Presentation{}, fmt.Errorf("modelingworkflow: artifact %q is not part of project", artifactID)
	}
	content, err := c.modeling.ReadArtifact(ctx, call, modelingapi.ReadArtifactRequest{
		ProjectID:  project.ID,
		ArtifactID: artifactID,
		Limit:      c.artifactLimit(),
	})
	if err != nil {
		return Presentation{}, err
	}
	if err := validateBoundedArtifact(content, artifactID, c.artifactLimit()); err != nil {
		return Presentation{}, err
	}
	if content.Artifact.Digest != descriptor.Digest || content.Artifact.Bytes != descriptor.Bytes {
		return Presentation{}, errors.New("modelingworkflow: artifact descriptor changed during read")
	}
	clonedContent := modelingapi.CloneArtifactContent(content)
	presentation := readOnlyProjectPresentation(project, "The requested artifact was read.")
	presentation.Content = &clonedContent
	return presentation, nil
}

func readOnlyProjectPresentation(project modelingapi.ProjectView, summary string) Presentation {
	state := stateFromProject(project)
	presentation := projectPresentation(state, summary, "", project)
	switch state {
	case StateNeedsInput:
		presentation.Question = "What additional information should be provided?"
	case StateFailed:
		if project.PublicError != nil {
			failure := modelingapi.ClonePublicError(*project.PublicError)
			presentation.Failure = &failure
		}
	}
	return presentation
}

func (c *Controller) artifactLimit() int {
	if c.artifactReadLimit > 0 && c.artifactReadLimit <= modelingapi.MaxPageSize {
		return c.artifactReadLimit
	}
	return defaultArtifactReadLimit
}

func (c *Controller) readBoundedArtifact(
	ctx context.Context,
	call CallContext,
	project modelingapi.ProjectID,
	artifact modelingapi.ArtifactDescriptor,
) (modelingapi.ArtifactContent, error) {
	content, err := c.modeling.ReadArtifact(ctx, flowContextToModelingAPI(call), modelingapi.ReadArtifactRequest{
		ProjectID:  project,
		ArtifactID: artifact.ID,
		Offset:     0,
		Limit:      c.artifactLimit(),
	})
	if err != nil {
		return modelingapi.ArtifactContent{}, err
	}
	if err := validateBoundedArtifact(content, artifact.ID, c.artifactLimit()); err != nil {
		return modelingapi.ArtifactContent{}, err
	}
	if content.Artifact.Digest != artifact.Digest || content.Artifact.Bytes != artifact.Bytes {
		return modelingapi.ArtifactContent{}, errors.New("modelingworkflow: artifact descriptor changed during read")
	}
	return modelingapi.CloneArtifactContent(content), nil
}

func validateBoundedArtifact(content modelingapi.ArtifactContent, expected modelingapi.ArtifactID, limit int) error {
	if content.Artifact.ID != expected {
		return errors.New("modelingworkflow: ReadArtifact returned a different artifact")
	}
	if err := modelingapi.ValidateArtifactContent(content); err != nil {
		return fmt.Errorf("modelingworkflow: invalid artifact content: %w", err)
	}
	if content.Offset != 0 || len(content.Data) > limit {
		return errors.New("modelingworkflow: artifact read exceeded bounded request")
	}
	if content.EOF {
		digest := sha256.Sum256(content.Data)
		if hex.EncodeToString(digest[:]) != content.Artifact.Digest {
			return errors.New("modelingworkflow: artifact digest verification failed")
		}
	}
	return nil
}

func findProjectArtifact(view modelingapi.ProjectView, id modelingapi.ArtifactID) (modelingapi.ArtifactDescriptor, bool) {
	for _, artifact := range view.Artifacts {
		if artifact.ID == id {
			return artifact, true
		}
	}
	return modelingapi.ArtifactDescriptor{}, false
}

func (c *Controller) resultFromPresentation(ctx context.Context, presentation Presentation) (Result, error) {
	if err := validatePresentation(presentation); err != nil {
		return Result{}, err
	}
	reply, err := c.Presenter.Present(ctx, presentation)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Reply:    reply,
		State:    presentation.State,
		Project:  cloneProjectPointer(presentation.Project),
		Artifact: cloneArtifactDescriptors(presentation.Artifacts),
		Evidence: cloneArtifactDescriptors(presentation.Evidence),
	}, nil
}

func childCall(
	parent CallContext,
	method string,
	project modelingapi.ProjectID,
	operation modelingapi.OperationName,
	ordinal int,
) modelingapi.CallContext {
	requestID := deriveIdentifier(parent.RequestID, method, string(project), string(operation), ordinal)
	idempotencyKey := ""
	if strings.TrimSpace(parent.IdempotencyKey) != "" {
		idempotencyKey = deriveIdentifier(parent.IdempotencyKey, method, string(project), string(operation), ordinal)
	}
	return modelingapi.CallContext{
		RequestID:      requestID,
		TraceID:        parent.TraceID,
		WorkspaceID:    parent.WorkspaceID,
		UserID:         parent.UserID,
		SessionID:      parent.SessionID,
		SessionKey:     parent.SessionKey,
		Channel:        parent.Channel,
		Interactive:    parent.Interactive,
		IdempotencyKey: idempotencyKey,
	}
}

func deriveIdentifier(parent, method, project, operation string, ordinal int) string {
	payload, _ := json.Marshal(struct {
		Version   int    `json:"version"`
		Parent    string `json:"parent"`
		Method    string `json:"method"`
		Project   string `json:"project"`
		Operation string `json:"operation"`
		Ordinal   int    `json:"ordinal"`
	}{
		Version:   1,
		Parent:    parent,
		Method:    method,
		Project:   project,
		Operation: operation,
		Ordinal:   ordinal,
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func validateCallContext(call CallContext) error {
	if strings.TrimSpace(call.SessionID) == "" {
		return errors.New("modelingworkflow: session_id is required")
	}
	return flowContextToModelingAPI(call).Validate(modelingapi.ReadOnly)
}

func validateRequest(req Request) error {
	if strings.TrimSpace(req.Text) == "" {
		return errors.New("modelingworkflow: request text is required")
	}
	return nil
}

func validatePresentation(p Presentation) error {
	switch p.State {
	case StateNeedsInput:
		if strings.TrimSpace(p.Question) == "" {
			return errors.New("modelingworkflow: needs_input presentation requires a question")
		}
	case StateWorking, StateAwaitingApply, StateCompleted:
		if p.Project == nil {
			return errors.New("modelingworkflow: project presentation is missing a project")
		}
	case StateFailed:
		if p.Failure == nil {
			return errors.New("modelingworkflow: failed presentation requires a public error")
		}
	default:
		return fmt.Errorf("modelingworkflow: invalid presentation state %q", p.State)
	}
	return nil
}

func generateBindingKey(call CallContext) BindingKey {
	return BindingKey{
		WorkspaceID:    call.WorkspaceID,
		UserID:         call.UserID,
		ConversationID: call.SessionID,
	}
}

func flowContextToModelingAPI(call CallContext) modelingapi.CallContext {
	return modelingapi.CallContext{
		RequestID:      call.RequestID,
		TraceID:        call.TraceID,
		WorkspaceID:    call.WorkspaceID,
		UserID:         call.UserID,
		SessionID:      call.SessionID,
		SessionKey:     call.SessionKey,
		Channel:        call.Channel,
		Interactive:    call.Interactive,
		IdempotencyKey: call.IdempotencyKey,
	}
}

func findOperation(capabilities modelingapi.Capabilities, name modelingapi.OperationName) (modelingapi.OperationDescriptor, bool) {
	for _, operation := range capabilities.Operations {
		if operation.Name == name {
			return operation, true
		}
	}
	return modelingapi.OperationDescriptor{}, false
}

func stateFromProject(project modelingapi.ProjectView) State {
	switch project.Status {
	case modelingapi.ProjectPending, modelingapi.ProjectRunning:
		return StateWorking
	case modelingapi.ProjectBlocked:
		if project.BlockedReason == "awaiting_apply" {
			return StateAwaitingApply
		}
		if project.PublicError != nil {
			return StateFailed
		}
		return StateNeedsInput
	case modelingapi.ProjectCompleted:
		return StateCompleted
	default:
		return StateFailed
	}
}

func projectPresentation(state State, summary, question string, project modelingapi.ProjectView) Presentation {
	cloned := modelingapi.CloneProjectView(project)
	return Presentation{
		State:     state,
		Summary:   summary,
		Question:  question,
		Project:   &cloned,
		Artifacts: cloneArtifactDescriptors(cloned.Artifacts),
	}
}

func missingProjectPresentation() Presentation {
	return Presentation{
		State:    StateNeedsInput,
		Summary:  "There is no active modeling project.",
		Question: "What would you like to model?",
	}
}

func cloneBinding(binding Binding) Binding {
	binding.Sources = modelingapi.CloneSources(binding.Sources)
	return binding
}

func cloneProjectPointer(project *modelingapi.ProjectView) *modelingapi.ProjectView {
	if project == nil {
		return nil
	}
	cloned := modelingapi.CloneProjectView(*project)
	return &cloned
}

func cloneArtifactDescriptors(in []modelingapi.ArtifactDescriptor) []modelingapi.ArtifactDescriptor {
	if in == nil {
		return nil
	}
	out := make([]modelingapi.ArtifactDescriptor, len(in))
	copy(out, in)
	return out
}

func (c *Controller) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now().UTC()
}
