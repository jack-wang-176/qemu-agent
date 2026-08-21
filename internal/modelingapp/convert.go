package modelingapp

import (
	"fmt"
	"sort"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

func engineViewToProjectView(in pipelineapi.EngineView) (modelingapi.ProjectView, error) {
	// TODO: copy descriptors and map status without exposing internal fields.
	status, err := projectStatusToAPI(in.Status)
	if err != nil {
		return modelingapi.ProjectView{}, err
	}
	descriptors := make([]modelingapi.OperationDescriptor, len(in.Recommended))
	for i, recommend := range in.Recommended {
		descriptor := operationDescriptorToAPI(recommend)
		descriptors[i] = descriptor
	}
	artifactsDes := make([]modelingapi.ArtifactDescriptor, len(in.Artifacts))
	for i, artifact := range in.Artifacts {
		artifactsDes[i] = artifactDescriptorToAPI(artifact)
	}

	view := modelingapi.ProjectView{
		ID:               modelingapi.ProjectID(in.ProjectID),
		Title:            in.Title,
		Revision:         in.Revision,
		Status:           status,
		CurrentOperation: modelingapi.OperationName(in.CurrentOperation),
		Recommended:      descriptors,
		Artifacts:        artifactsDes,
		EvidenceCount:    in.EvidenceCount,
		CreatedAt:        in.CreatedAt,
		UpdatedAt:        in.UpdatedAt,
	}
	if in.LastError != "" {
		public := publicErrorForCategory(in.LastError)
		view.PublicError = &public
	}
	if err := modelingapi.ValidateProjectView(view); err != nil {
		return modelingapi.ProjectView{}, fmt.Errorf("modelingapp: project view projection: %w", err)
	}
	return modelingapi.CloneProjectView(view), nil
}

func projectStatusToAPI(status pipelineapi.ProjectStatus) (modelingapi.ProjectStatus, error) {
	switch status {
	case pipelineapi.StatusPending:
		return modelingapi.ProjectPending, nil
	case pipelineapi.StatusRunning:
		return modelingapi.ProjectRunning, nil
	case pipelineapi.StatusBlocked:
		return modelingapi.ProjectBlocked, nil
	case pipelineapi.StatusDone:
		return modelingapi.ProjectCompleted, nil
	default:
		return "", fmt.Errorf(
			"modelingapp: unknown pipeline project status %q",
			status,
		)
	}
}

func artifactDescriptorToAPI(in pipelineapi.ArtifactDescriptor) modelingapi.ArtifactDescriptor {
	return modelingapi.ArtifactDescriptor{
		ID:        modelingapi.ArtifactID(in.ID),
		Operation: modelingapi.OperationName(in.Operation),
		Name:      in.Name,
		Kind:      in.Kind,
		Bytes:     in.Bytes,
		Digest:    in.Digest,
		CreatedAt: in.CreatedAt,
	}
}

func operationDescriptorToAPI(in pipelineapi.OperationDescriptor) modelingapi.OperationDescriptor {
	return modelingapi.OperationDescriptor{
		Name:            modelingapi.OperationName(in.Name),
		DisplayName:     in.DisplayName,
		Description:     in.Description,
		RequiresSources: in.RequiresSources,
		Mutating:        in.Mutating,
		MayBlock:        in.MayBlock,
	}
}

func descriptionToCapabilities(in pipelineapi.Description) (modelingapi.Capabilities, error) {
	operations := make([]modelingapi.OperationDescriptor, len(in.Operations))
	for i, operation := range in.Operations {
		operations[i] = operationDescriptorToAPI(operation)
	}
	operations = modelingapi.SortedOperations(operations)
	kinds := append([]string(nil), in.ArtifactKinds...)
	sort.Strings(kinds)
	out := modelingapi.Capabilities{APIVersion: in.APIVersion, EngineName: in.EngineName, EngineVersion: in.EngineVersion, Operations: operations, ArtifactKinds: kinds, SupportsApply: in.SupportsApply, SupportsEvidence: in.SupportsEvidence, SupportsCancel: in.SupportsCancel, SupportsProgress: in.SupportsProgress}
	if err := modelingapi.ValidateCapabilities(out); err != nil {
		return modelingapi.Capabilities{}, fmt.Errorf("modelingapp: capabilities projection: %w", err)
	}
	return modelingapi.CloneCapabilities(out), nil
}

func sourceRefToPipeline(in modelingapi.SourceRef) pipelineapi.SourceRef {
	return pipelineapi.SourceRef{Kind: in.Kind, Value: in.Value, Digest: in.Digest}
}

func executeResultToAPI(in pipelineapi.ExecuteResult) (modelingapi.OperationResult, error) {
	project, err := engineViewToProjectView(in.Project)
	if err != nil {
		return modelingapi.OperationResult{}, err
	}
	status, err := operationStatusToAPI(in.Status)
	if err != nil {
		return modelingapi.OperationResult{}, err
	}
	out := modelingapi.OperationResult{Project: project, Operation: modelingapi.OperationName(in.Operation), Status: status, Artifacts: artifactDescriptorsToAPI(in.Artifacts), Evidence: artifactDescriptorsToAPI(in.Evidence), Summary: in.Summary, Blocked: in.Blocked, Reason: in.Reason}
	if in.Apply != nil {
		out.Apply = &modelingapi.ApplyOutcome{Written: append([]string(nil), in.Apply.Written...), Skipped: append([]string(nil), in.Apply.Skipped...), Partial: in.Apply.Partial, Reason: in.Apply.Reason, Evidence: artifactDescriptorsToAPI(in.Apply.Evidence)}
	}
	if err := modelingapi.ValidateOperationResult(out); err != nil {
		return modelingapi.OperationResult{}, fmt.Errorf("modelingapp: operation result projection: %w", err)
	}
	return modelingapi.CloneOperationResult(out), nil
}

func artifactDescriptorsToAPI(in []pipelineapi.ArtifactDescriptor) []modelingapi.ArtifactDescriptor {
	out := make([]modelingapi.ArtifactDescriptor, len(in))
	for i, value := range in {
		out[i] = artifactDescriptorToAPI(value)
	}
	return out
}

func operationStatusToAPI(in pipelineapi.OperationStatus) (modelingapi.OperationStatus, error) {
	switch in {
	case pipelineapi.OpSucceeded:
		return modelingapi.OperationSucceeded, nil
	case pipelineapi.OpBlocked:
		return modelingapi.OperationBlocked, nil
	case pipelineapi.OpFailed:
		return modelingapi.OperationFailed, nil
	default:
		return "", fmt.Errorf("modelingapp: unknown operation status %q", in)
	}
}

func recordToEngineView(in pipelineapi.ProjectRecord) pipelineapi.EngineView {
	return pipelineapi.EngineView{ProjectID: in.ID, Title: in.Title, Revision: in.Revision, Status: in.Status, CurrentOperation: in.Current, Artifacts: append([]pipelineapi.ArtifactDescriptor(nil), in.Artifacts...), EvidenceCount: len(in.Evidence), LastError: in.LastError, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt}
}

func artifactContentToAPI(in pipelineapi.ArtifactContent) (modelingapi.ArtifactContent, error) {
	out := modelingapi.ArtifactContent{Artifact: artifactDescriptorToAPI(in.Artifact), Data: append([]byte(nil), in.Data...), Offset: in.Offset, Next: in.Next, EOF: in.EOF}
	if err := modelingapi.ValidateArtifactContent(out); err != nil {
		return modelingapi.ArtifactContent{}, fmt.Errorf("modelingapp: artifact content projection: %w", err)
	}
	return modelingapi.CloneArtifactContent(out), nil
}
