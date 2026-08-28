package modelingworkflow

import (
	"context"
	"errors"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

// selectReviewArtifact returns the single diff exposed by the project view.
// Ambiguous views are rejected so descriptor ordering cannot change behavior.
func selectReviewArtifact(view modelingapi.ProjectView) (modelingapi.ArtifactDescriptor, bool) {
	var selected modelingapi.ArtifactDescriptor
	found := 0
	for _, artifact := range view.Artifacts {
		if artifact.Kind != "diff" {
			continue
		}
		if view.CurrentOperation != "" && artifact.Operation != view.CurrentOperation {
			continue
		}
		selected = artifact
		found++
	}
	if found == 1 {
		return selected, true
	}
	// A view may expose a diff from a completed operation while the current
	// operation has already moved on. In that case accept it only if unique.
	if found == 0 {
		for _, artifact := range view.Artifacts {
			if artifact.Kind != "diff" {
				continue
			}
			selected = artifact
			found++
		}
	}
	return selected, found == 1
}

func awaitingApplyPresentation(
	c *Controller,
	ctx context.Context,
	call CallContext,
	view modelingapi.ProjectView,
	artifacts []modelingapi.ArtifactDescriptor,
	evidence []modelingapi.ArtifactDescriptor,
) (Presentation, error) {
	diff, ok := selectReviewArtifact(view)
	if !ok {
		return Presentation{}, errors.New("modelingworkflow: awaiting_apply requires one unambiguous diff artifact")
	}
	content, err := c.readBoundedArtifact(ctx, call, view.ID, diff)
	if err != nil {
		return Presentation{}, err
	}
	presentation := projectPresentation(
		StateAwaitingApply,
		"The modeling operation produced a diff and is awaiting review before apply.",
		"Review the bounded diff and explicitly approve before applying changes.",
		view,
	)
	presentation.Content = &content
	presentation.Artifacts = mergeArtifactDescriptors(presentation.Artifacts, artifacts)
	presentation.Evidence = cloneArtifactDescriptors(evidence)
	return presentation, nil
}

func mergeArtifactDescriptors(groups ...[]modelingapi.ArtifactDescriptor) []modelingapi.ArtifactDescriptor {
	seen := make(map[modelingapi.ArtifactID]struct{})
	var merged []modelingapi.ArtifactDescriptor
	for _, group := range groups {
		for _, artifact := range group {
			if _, ok := seen[artifact.ID]; ok {
				continue
			}
			seen[artifact.ID] = struct{}{}
			merged = append(merged, artifact)
		}
	}
	return merged
}
