package modelingworkflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

func (c *Controller) advanceUntilBoundary(
	ctx context.Context,
	call CallContext,
	binding Binding,
	view modelingapi.ProjectView,
	capabilities modelingapi.Capabilities,
) (Presentation, error) {
	if err := ctx.Err(); err != nil {
		return Presentation{}, err
	}
	if err := modelingapi.ValidateProjectView(view); err != nil {
		return Presentation{}, fmt.Errorf("modelingworkflow: invalid starting project: %w", err)
	}
	if err := flowContextToModelingAPI(call).Validate(modelingapi.Mutating); err != nil {
		return Presentation{}, err
	}

	limit := c.maxOperations
	if limit <= 0 {
		limit = 1
	}
	var artifacts []modelingapi.ArtifactDescriptor
	var evidence []modelingapi.ArtifactDescriptor

	for ordinal := 0; ordinal < limit; ordinal++ {
		if err := ctx.Err(); err != nil {
			return Presentation{}, err
		}

		// Stop before mutation when the current project is already at a boundary.
		switch view.Status {
		case modelingapi.ProjectCompleted:
			return withOperationOutputs(
				projectPresentation(StateCompleted, "The modeling project is complete.", "", view),
				artifacts,
				evidence,
			), nil
		case modelingapi.ProjectBlocked:
			presentation := projectPresentation(
				StateNeedsInput,
				"The modeling project is blocked.",
				"What additional information or approval should be provided?",
				view,
			)
			if view.PublicError != nil {
				failure := modelingapi.ClonePublicError(*view.PublicError)
				presentation.State = StateFailed
				presentation.Question = ""
				presentation.Failure = &failure
			}
			return withOperationOutputs(presentation, artifacts, evidence), nil
		}

		op := view.CurrentOperation
		if strings.TrimSpace(string(op)) == "" {
			return Presentation{}, errors.New("modelingworkflow: active project has no current operation")
		}
		operation, supported := findOperation(capabilities, op)
		if !supported {
			return Presentation{}, fmt.Errorf("modelingworkflow: engine does not expose operation %q", op)
		}
		if operation.RequiresSources && len(binding.Sources) == 0 {
			updated := cloneBinding(binding)
			updated.Awaiting = AwaitingSource
			updated.UpdatedAt = c.currentTime()
			saved, err := c.binding.CompareAndSave(ctx, updated, binding.Version)
			if err != nil {
				return Presentation{}, fmt.Errorf("modelingworkflow: save source wait state: %w", err)
			}
			binding = saved
			return withOperationOutputs(
				projectPresentation(
					StateNeedsInput,
					"The next modeling operation requires source material.",
					"Which workspace source should be used?",
					view,
				),
				artifacts,
				evidence,
			), nil
		}

		revision := view.Revision
		atomicCall := childCall(call, "Advance", view.ID, op, ordinal)
		result, err := c.modeling.Advance(ctx, atomicCall, modelingapi.AdvanceRequest{
			ProjectID:        view.ID,
			Operation:        op,
			Instruction:      binding.Instruction,
			Sources:          modelingapi.CloneSources(binding.Sources),
			ExpectedRevision: revision,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return Presentation{}, err
			}
			var apiErr *modelingapi.Error
			if !errors.As(err, &apiErr) {
				return Presentation{}, err
			}

			// A mutation error may still have committed a terminal project state.
			// Re-inspect once, but never replay Advance inside this turn. Only a
			// state proven by the newer Project is presented as a persisted result;
			// conflicts and transport failures remain typed workflow errors.
			latest, showErr := c.modeling.Show(ctx, flowContextToModelingAPI(call), modelingapi.ShowRequest{
				ProjectID: view.ID,
			})
			if showErr == nil && latest.ID == view.ID && latest.Revision > revision {
				if err := modelingapi.ValidateProjectView(latest); err != nil {
					return Presentation{}, fmt.Errorf("modelingworkflow: invalid project after Advance failure: %w", err)
				}
				view = modelingapi.CloneProjectView(latest)
				if view.BlockedReason == "awaiting_apply" {
					return awaitingApplyPresentation(c, ctx, call, view, artifacts, evidence)
				}
				if view.Status == modelingapi.ProjectBlocked && view.PublicError == nil {
					presentation := projectPresentation(
						StateNeedsInput,
						"The modeling operation stopped at an input boundary.",
						"What additional information should be provided?",
						view,
					)
					return withOperationOutputs(presentation, artifacts, evidence), nil
				}
				if view.PublicError != nil {
					failure := modelingapi.ClonePublicError(*view.PublicError)
					presentation := projectPresentation(StateFailed, failure.Message, "", view)
					presentation.Failure = &failure
					return withOperationOutputs(presentation, artifacts, evidence), nil
				}
			}
			return Presentation{}, apiErr
		}

		if err := validateAdvanceResult(result, view.ID, op, revision); err != nil {
			return Presentation{}, err
		}
		artifacts = append(artifacts, cloneArtifactDescriptors(result.Artifacts)...)
		evidence = append(evidence, cloneArtifactDescriptors(result.Evidence)...)
		view = modelingapi.CloneProjectView(result.Project)

		switch result.Status {
		case modelingapi.OperationBlocked:
			if result.Reason == "awaiting_apply" {
				return awaitingApplyPresentation(c, ctx, call, view, artifacts, evidence)
			}
			presentation := projectPresentation(
				StateNeedsInput,
				"The modeling operation is waiting at a workflow boundary.",
				"What additional information or approval should be provided?",
				view,
			)
			return withOperationOutputs(presentation, artifacts, evidence), nil

		case modelingapi.OperationFailed:
			failure := view.PublicError
			if failure == nil {
				public := modelingapi.NewPublicError(
					modelingapi.ErrorInternal,
					"The modeling operation could not be completed.",
					false,
					nil,
				)
				failure = &public
			}
			clonedFailure := modelingapi.ClonePublicError(*failure)
			presentation := projectPresentation(StateFailed, clonedFailure.Message, "", view)
			presentation.Failure = &clonedFailure
			return withOperationOutputs(presentation, artifacts, evidence), nil
		}
	}

	return withOperationOutputs(
		projectPresentation(
			StateWorking,
			"The modeling project paused after reaching this turn's operation limit.",
			"",
			view,
		),
		artifacts,
		evidence,
	), nil
}

func validateAdvanceResult(
	result modelingapi.OperationResult,
	projectID modelingapi.ProjectID,
	operation modelingapi.OperationName,
	previousRevision int,
) error {
	if err := modelingapi.ValidateOperationResult(result); err != nil {
		return fmt.Errorf("modelingworkflow: invalid Advance result: %w", err)
	}
	if result.Project.ID != projectID {
		return errors.New("modelingworkflow: Advance returned a different project")
	}
	if result.Operation != operation {
		return fmt.Errorf(
			"modelingworkflow: Advance returned operation %q for requested operation %q",
			result.Operation,
			operation,
		)
	}
	if result.Project.Revision <= previousRevision {
		return fmt.Errorf(
			"modelingworkflow: Advance revision did not increase: previous=%d current=%d",
			previousRevision,
			result.Project.Revision,
		)
	}
	return nil
}

func withOperationOutputs(
	presentation Presentation,
	artifacts []modelingapi.ArtifactDescriptor,
	evidence []modelingapi.ArtifactDescriptor,
) Presentation {
	presentation.Artifacts = cloneArtifactDescriptors(artifacts)
	presentation.Evidence = cloneArtifactDescriptors(evidence)
	return presentation
}
