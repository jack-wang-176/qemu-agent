package modelingapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

type Service struct {
	runtimePorts pipelineapi.RuntimePorts
	queryPort    pipelineapi.QueryPort
	engine       pipelineapi.Engine
}

type Dependencies struct {
	RuntimePorts pipelineapi.RuntimePorts
	QueryPort    pipelineapi.QueryPort
	Engine       pipelineapi.Engine
}

func NewService(deps Dependencies) (*Service, error) {
	if err := pipelineapi.ValidatePorts(deps.RuntimePorts); err != nil {
		return nil, fmt.Errorf("modelingapp: invalid runtime ports: %w", err)
	}
	if deps.QueryPort == nil {
		return nil, errors.New("modelingapp: query port is nil")
	}
	if deps.Engine == nil {
		return nil, errors.New("modelingapp: engine is nil")
	}
	return &Service{
		runtimePorts: deps.RuntimePorts,
		queryPort:    deps.QueryPort,
		engine:       deps.Engine,
	}, nil
}

var _ modelingapi.Service = (*Service)(nil)

func (s *Service) Show(ctx context.Context, call modelingapi.CallContext, req modelingapi.ShowRequest) (modelingapi.ProjectView, error) {
	if err := modelingapi.ValidateShowRequest(req); err != nil {
		return modelingapi.ProjectView{}, err
	}
	scope, _, err := deriveContext(call, "Show")
	if err != nil {
		return modelingapi.ProjectView{}, mapInternalError(err)
	}

	engineView, err := s.engine.Inspect(ctx, pipelineapi.InspectRequest{
		Scope:     scope,
		ProjectID: pipelineapi.ProjectID(req.ProjectID),
		Revision:  0,
	})
	if err != nil {
		return modelingapi.ProjectView{}, err
	}
	projectView, err := engineViewToProjectView(engineView)
	if err != nil {
		return modelingapi.ProjectView{}, err
	}
	return projectView, nil
}

func (s *Service) Capabilities(ctx context.Context, call modelingapi.CallContext) (modelingapi.Capabilities, error) {
	scope, _, err := deriveContext(call, "Capabilities")
	if err != nil {
		return modelingapi.Capabilities{}, mapInternalError(err)
	}
	desc, err := s.engine.Describe(ctx, pipelineapi.DescribeRequest{
		Scope: scope,
	})
	if err != nil {
		return modelingapi.Capabilities{}, err
	}
	capabilities, err := descriptionToCapabilities(desc)
	if err != nil {
		return modelingapi.Capabilities{}, err
	}
	return capabilities, nil
}

func (s *Service) Create(ctx context.Context, call modelingapi.CallContext, req modelingapi.CreateRequest) (modelingapi.ProjectView, error) {
	if err := modelingapi.ValidateCreateRequest(req); err != nil {
		return modelingapi.ProjectView{}, err
	}
	scope, _, err := deriveContext(call, "Create")
	if err != nil {
		return modelingapi.ProjectView{}, err
	}
	record, err := s.runtimePorts.Repository.CreateProject(ctx, pipelineapi.ProjectRecord{Scope: scope, Title: req.Title, Current: "plan", Status: pipelineapi.StatusPending})
	if err != nil {
		return modelingapi.ProjectView{}, mapInternalError(err)
	}
	view, err := engineViewToProjectView(recordToEngineView(record))
	if err != nil {
		return modelingapi.ProjectView{}, mapInternalError(err)
	}
	return view, nil
}

func (s *Service) List(ctx context.Context, call modelingapi.CallContext, req modelingapi.ListRequest) (modelingapi.ProjectPage, error) {
	if err := modelingapi.ValidateListRequest(req); err != nil {
		return modelingapi.ProjectPage{}, err
	}
	scope, _, err := deriveContext(call, "List")
	if err != nil {
		return modelingapi.ProjectPage{}, err
	}
	page, err := s.queryPort.List(ctx, pipelineapi.ListQuery{Scope: scope, Limit: req.Limit, Cursor: req.Cursor})
	if err != nil {
		return modelingapi.ProjectPage{}, mapInternalError(err)
	}
	out := modelingapi.ProjectPage{Projects: make([]modelingapi.ProjectView, len(page.Projects)), NextCursor: page.NextCursor}
	for i, project := range page.Projects {
		out.Projects[i], err = engineViewToProjectView(project)
		if err != nil {
			return modelingapi.ProjectPage{}, mapInternalError(err)
		}
	}
	if err := modelingapi.ValidateProjectPage(out); err != nil {
		return modelingapi.ProjectPage{}, mapInternalError(err)
	}
	return modelingapi.CloneProjectPage(out), nil
}

func (s *Service) Advance(ctx context.Context, call modelingapi.CallContext, req modelingapi.AdvanceRequest) (modelingapi.OperationResult, error) {
	if err := modelingapi.ValidateAdvanceRequest(req); err != nil {
		return modelingapi.OperationResult{}, err
	}
	request, err := s.buildExecuteRequest(ctx, call, req)
	if err != nil {
		return modelingapi.OperationResult{}, err
	}
	result, err := s.engine.Execute(ctx, request)
	if err != nil {
		return modelingapi.OperationResult{}, mapInternalError(err)
	}
	converted, err := executeResultToAPI(result)
	if err != nil {
		return modelingapi.OperationResult{}, mapInternalError(err)
	}
	return converted, nil
}

func (s *Service) buildExecuteRequest(
	ctx context.Context,
	call modelingapi.CallContext,
	req modelingapi.AdvanceRequest,
) (pipelineapi.ExecuteRequest, error) {
	scope, invocation, err := deriveContext(call, "Advance")
	if err != nil {
		return pipelineapi.ExecuteRequest{}, err
	}
	sources := make([]pipelineapi.SourceRef, len(req.Sources))
	for i, source := range req.Sources {
		sources[i] = sourceRefToPipeline(source)
	}
	project, err := s.engine.Inspect(ctx, pipelineapi.InspectRequest{Scope: scope, ProjectID: pipelineapi.ProjectID(req.ProjectID), Revision: req.ExpectedRevision})
	if err != nil {
		return pipelineapi.ExecuteRequest{}, mapInternalError(err)
	}
	if req.ExpectedRevision > 0 && project.Revision != req.ExpectedRevision {
		return pipelineapi.ExecuteRequest{}, &modelingapi.Error{Public: modelingapi.NewPublicError(modelingapi.ErrorConflict, "The modeling project changed before the operation started.", true, map[string]string{"current_revision": fmt.Sprint(project.Revision), "expected_revision": fmt.Sprint(req.ExpectedRevision)}), Cause: errors.New("modelingapp: expected revision does not match current project")}
	}
	operation := pipelineapi.OperationName(req.Operation)
	if operation == "" {
		operation = project.CurrentOperation
	}
	request := pipelineapi.ExecuteRequest{
		Project: pipelineapi.ProjectSnapshot{
			ID:       pipelineapi.ProjectID(req.ProjectID),
			Revision: project.Revision,
			Scope:    scope,
		},
		Operation:        operation,
		Instruction:      req.Instruction,
		ExpectedRevision: req.ExpectedRevision,
		Invocation:       invocation,
		Ports:            s.runtimePorts,
		Sources:          sources,
	}
	if err := request.Validate(); err != nil {
		return pipelineapi.ExecuteRequest{}, err
	}
	return request.Clone(), nil
}

func (s *Service) Reset(_ context.Context, call modelingapi.CallContext, req modelingapi.ResetRequest) (modelingapi.ProjectView, error) {
	if err := modelingapi.ValidateResetRequest(req); err != nil {
		return modelingapi.ProjectView{}, err
	}
	if _, _, err := deriveContext(call, "Reset"); err != nil {
		return modelingapi.ProjectView{}, err
	}
	return modelingapi.ProjectView{}, unsupported("reset")
}

func (s *Service) PlanApply(_ context.Context, call modelingapi.CallContext, req modelingapi.PlanApplyRequest) (modelingapi.ApplyPreview, error) {
	if err := modelingapi.ValidatePlanApplyRequest(req); err != nil {
		return modelingapi.ApplyPreview{}, err
	}
	if _, _, err := deriveContext(call, "PlanApply"); err != nil {
		return modelingapi.ApplyPreview{}, err
	}
	return modelingapi.ApplyPreview{}, unsupported("plan_apply")
}

func (s *Service) Apply(_ context.Context, call modelingapi.CallContext, req modelingapi.ApplyRequest) (modelingapi.OperationResult, error) {
	if err := modelingapi.ValidateApplyRequest(req); err != nil {
		return modelingapi.OperationResult{}, err
	}
	if _, _, err := deriveContext(call, "Apply"); err != nil {
		return modelingapi.OperationResult{}, err
	}
	return modelingapi.OperationResult{}, unsupported("apply")
}

func (s *Service) ReadArtifact(ctx context.Context, call modelingapi.CallContext, req modelingapi.ReadArtifactRequest) (modelingapi.ArtifactContent, error) {
	if err := modelingapi.ValidateReadArtifactRequest(req); err != nil {
		return modelingapi.ArtifactContent{}, err
	}
	scope, _, err := deriveContext(call, "ReadArtifact")
	if err != nil {
		return modelingapi.ArtifactContent{}, err
	}
	content, err := s.queryPort.ReadArtifact(ctx, pipelineapi.ArtifactQuery{Scope: scope, ProjectID: pipelineapi.ProjectID(req.ProjectID), ArtifactID: pipelineapi.ArtifactID(req.ArtifactID), Offset: req.Offset, Limit: req.Limit})
	if err != nil {
		return modelingapi.ArtifactContent{}, mapInternalError(err)
	}
	out, err := artifactContentToAPI(content)
	if err != nil {
		return modelingapi.ArtifactContent{}, mapInternalError(err)
	}
	return out, nil
}

func (s *Service) Evidence(ctx context.Context, call modelingapi.CallContext, req modelingapi.EvidenceRequest) (modelingapi.EvidencePage, error) {
	if err := modelingapi.ValidateEvidenceRequest(req); err != nil {
		return modelingapi.EvidencePage{}, err
	}
	scope, _, err := deriveContext(call, "Evidence")
	if err != nil {
		return modelingapi.EvidencePage{}, err
	}
	page, err := s.queryPort.Evidence(ctx, pipelineapi.EvidenceQuery{Scope: scope, ProjectID: pipelineapi.ProjectID(req.ProjectID), Limit: req.Limit, Cursor: req.Cursor})
	if err != nil {
		return modelingapi.EvidencePage{}, mapInternalError(err)
	}
	out := modelingapi.EvidencePage{Evidence: artifactDescriptorsToAPI(page.Artifacts), NextCursor: page.NextCursor}
	if err := modelingapi.ValidateEvidencePage(out); err != nil {
		return modelingapi.EvidencePage{}, mapInternalError(err)
	}
	return modelingapi.CloneEvidencePage(out), nil
}
