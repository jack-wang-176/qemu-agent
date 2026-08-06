package modelingapi

// clone.go — defensive copy helpers。
//
// Design principles (v1-06, section 3.4):
//   - Every slice, map, and byte payload is defensively copied.
//   - Caller mutations cannot affect stores or other callers.
//   - Shared clone helpers prevent adapters from reimplementing copy rules.

// CloneCapabilities returns a deep copy of Capabilities.
func CloneCapabilities(c Capabilities) Capabilities {
	return Capabilities{
		APIVersion:       c.APIVersion,
		EngineName:       c.EngineName,
		EngineVersion:    c.EngineVersion,
		Operations:       cloneOperationDescriptors(c.Operations),
		ArtifactKinds:    cloneStringSlice(c.ArtifactKinds),
		SupportsApply:    c.SupportsApply,
		SupportsEvidence: c.SupportsEvidence,
		SupportsCancel:   c.SupportsCancel,
		SupportsProgress: c.SupportsProgress,
	}
}

func cloneOperationDescriptors(ops []OperationDescriptor) []OperationDescriptor {
	if ops == nil {
		return nil
	}
	out := make([]OperationDescriptor, len(ops))
	copy(out, ops)
	return out
}

func cloneStringSlice(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// CloneProjectView returns a deep copy of ProjectView.
func CloneProjectView(v ProjectView) ProjectView {
	return ProjectView{
		ID:               v.ID,
		Title:            v.Title,
		Revision:         v.Revision,
		Status:           v.Status,
		CurrentOperation: v.CurrentOperation,
		Recommended:      cloneOperationDescriptors(v.Recommended),
		Artifacts:        cloneArtifactDescriptors(v.Artifacts),
		EvidenceCount:    v.EvidenceCount,
		PublicError:      clonePublicError(v.PublicError),
		CreatedAt:        v.CreatedAt,
		UpdatedAt:        v.UpdatedAt,
	}
}

func cloneArtifactDescriptors(as []ArtifactDescriptor) []ArtifactDescriptor {
	if as == nil {
		return nil
	}
	out := make([]ArtifactDescriptor, len(as))
	copy(out, as)
	return out
}

func clonePublicError(p *PublicError) *PublicError {
	if p == nil {
		return nil
	}
	details := make(map[string]string, len(p.Details))
	for k, v := range p.Details {
		details[k] = v
	}
	return &PublicError{
		Code:      p.Code,
		Message:   p.Message,
		Retryable: p.Retryable,
		Details:   details,
	}
}

// ClonePublicError returns a deep copy of PublicError.
func ClonePublicError(p PublicError) PublicError {
	cloned := clonePublicError(&p)
	return *cloned
}

// CloneOperationResult returns a deep copy of OperationResult.
func CloneOperationResult(r OperationResult) OperationResult {
	return OperationResult{
		Project:   CloneProjectView(r.Project),
		Operation: r.Operation,
		Status:    r.Status,
		Artifacts: cloneArtifactDescriptors(r.Artifacts),
		Evidence:  cloneArtifactDescriptors(r.Evidence),
		Summary:   r.Summary,
		Blocked:   r.Blocked,
		Reason:    r.Reason,
		Apply:     cloneApplyOutcome(r.Apply),
	}
}

func cloneApplyOutcome(in *ApplyOutcome) *ApplyOutcome {
	if in == nil {
		return nil
	}
	return &ApplyOutcome{
		Written:  cloneStringSlice(in.Written),
		Skipped:  cloneStringSlice(in.Skipped),
		Partial:  in.Partial,
		Reason:   in.Reason,
		Evidence: cloneArtifactDescriptors(in.Evidence),
	}
}

// CloneArtifactContent returns a deep copy of ArtifactContent. Data is always a
// new slice, so caller mutations cannot affect stored content.
func CloneArtifactContent(c ArtifactContent) ArtifactContent {
	var data []byte
	if c.Data != nil {
		data = make([]byte, len(c.Data))
		copy(data, c.Data)
	}
	return ArtifactContent{
		Artifact: c.Artifact,
		Data:     data,
		Offset:   c.Offset,
		Next:     c.Next,
		EOF:      c.EOF,
	}
}

// CloneApplyPreview returns a deep copy of ApplyPreview.
func CloneApplyPreview(p ApplyPreview) ApplyPreview {
	return ApplyPreview{
		ID:              p.ID,
		ProjectID:       p.ProjectID,
		ProjectRevision: p.ProjectRevision,
		Diff:            p.Diff,
		Files:           cloneFileChanges(p.Files),
		Summary:         p.Summary,
		ExpiresAt:       p.ExpiresAt,
	}
}

func cloneFileChanges(fs []FileChange) []FileChange {
	if fs == nil {
		return nil
	}
	out := make([]FileChange, len(fs))
	copy(out, fs)
	return out
}

// CloneProjectPage returns a deep copy of ProjectPage.
func CloneProjectPage(p ProjectPage) ProjectPage {
	out := ProjectPage{
		NextCursor: p.NextCursor,
	}
	if p.Projects != nil {
		out.Projects = make([]ProjectView, len(p.Projects))
		for i, v := range p.Projects {
			out.Projects[i] = CloneProjectView(v)
		}
	}
	return out
}

// CloneEvidencePage returns a deep copy of EvidencePage.
func CloneEvidencePage(p EvidencePage) EvidencePage {
	return EvidencePage{
		Evidence:   cloneArtifactDescriptors(p.Evidence),
		NextCursor: p.NextCursor,
	}
}

// CloneSources returns a copy of a SourceRef slice.
func CloneSources(s []SourceRef) []SourceRef {
	if s == nil {
		return nil
	}
	out := make([]SourceRef, len(s))
	copy(out, s)
	return out
}

// CloneAdvanceRequest returns a deep copy of AdvanceRequest.
func CloneAdvanceRequest(r AdvanceRequest) AdvanceRequest {
	return AdvanceRequest{
		ProjectID:        r.ProjectID,
		Operation:        r.Operation,
		Instruction:      r.Instruction,
		Sources:          CloneSources(r.Sources),
		ExpectedRevision: r.ExpectedRevision,
	}
}

// CloneEvent returns a deep copy of Event and its nested payloads.
func CloneEvent(e Event) Event {
	out := Event{
		Kind:      e.Kind,
		ProjectID: e.ProjectID,
		Operation: e.Operation,
		Error:     clonePublicError(e.Error),
	}
	if e.Progress != nil {
		progress := *e.Progress
		out.Progress = &progress
	}
	if e.Result != nil {
		out.Result = &ResultSummary{
			Status:    e.Result.Status,
			Summary:   e.Result.Summary,
			Artifacts: cloneArtifactDescriptors(e.Result.Artifacts),
			Evidence:  cloneArtifactDescriptors(e.Result.Evidence),
		}
	}
	return out
}
