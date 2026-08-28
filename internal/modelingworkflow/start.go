package modelingworkflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

func (c *Controller) start(
	ctx context.Context,
	call CallContext,
	key BindingKey,
	current Binding,
	found bool,
	replace bool,
	capabilities modelingapi.Capabilities,
	intent Intent,
) (Presentation, error) {
	// Phase 1: reject cancellation, mismatched bindings, and implicit replacement.
	if err := ctx.Err(); err != nil {
		return Presentation{}, err
	}
	if found && current.Key != (BindingKey{}) && current.Key != key {
		return Presentation{}, errors.New("modelingworkflow: loaded binding key does not match the call")
	}
	if found && current.ActiveProjectID != "" && !replace {
		return Presentation{}, errors.New("modelingworkflow: start cannot replace an active project")
	}

	next, expected, err := collectStartInput(key, current, found, replace, intent)
	if err != nil {
		return Presentation{}, err
	}
	// Phase 2: keep incomplete requirements in the binding; do not create a project.
	missingTitle := next.Title == ""
	missingInstruction := next.Instruction == ""
	if missingTitle || missingInstruction {
		next.Awaiting = AwaitingRequirement
		next.UpdatedAt = c.currentTime()
		if _, err := c.binding.CompareAndSave(ctx, next, expected); err != nil {
			return Presentation{}, fmt.Errorf("modelingworkflow: save modeling intake: %w", err)
		}
		return Presentation{
			State:    StateNeedsInput,
			Summary:  "More information is required before the modeling project can be created.",
			Question: missingRequirementQuestion(missingTitle, missingInstruction),
		}, nil
	}

	// The parent operation key must exist before deriving the stable Create key.
	if err := flowContextToModelingAPI(call).Validate(modelingapi.Mutating); err != nil {
		return Presentation{}, err
	}
	// Phase 3: create exactly one project using a stable atomic call identity.
	createCall := childCall(call, "Create", "", "", 0)
	created, err := c.modeling.Create(ctx, createCall, modelingapi.CreateRequest{Title: next.Title})
	if err != nil {
		return Presentation{}, err
	}
	if err := modelingapi.ValidateProjectView(created); err != nil {
		return Presentation{}, fmt.Errorf("modelingworkflow: Create returned an invalid project: %w", err)
	}

	next.ActiveProjectID = created.ID
	next.Awaiting = AwaitingNone
	// Phase 4: persist the new project binding and record any required source wait.
	if operation, ok := findOperation(capabilities, created.CurrentOperation); !ok {
		// Persist the ProjectID before reporting a dependency contract error. Create
		// has already committed and cannot be rolled back by the workflow binding.
		next.UpdatedAt = c.currentTime()
		if _, saveErr := c.binding.CompareAndSave(ctx, next, expected); saveErr != nil {
			return Presentation{}, errors.Join(
				fmt.Errorf("modelingworkflow: engine does not expose created operation %q", created.CurrentOperation),
				fmt.Errorf("save created project binding: %w", saveErr),
			)
		}
		return Presentation{}, fmt.Errorf("modelingworkflow: engine does not expose created operation %q", created.CurrentOperation)
	} else if operation.RequiresSources && len(next.Sources) == 0 {
		next.Awaiting = AwaitingSource
	}
	next.UpdatedAt = c.currentTime()
	if _, err := c.binding.CompareAndSave(ctx, next, expected); err != nil {
		return Presentation{}, fmt.Errorf(
			"modelingworkflow: project %q was created but its binding could not be saved: %w",
			created.ID,
			err,
		)
	}

	project, err := c.modeling.Show(ctx, flowContextToModelingAPI(call), modelingapi.ShowRequest{
		ProjectID: created.ID,
	})
	if err != nil {
		return Presentation{}, err
	}
	if project.ID != created.ID {
		return Presentation{}, errors.New("modelingworkflow: Show returned a different project after Create")
	}
	if err := modelingapi.ValidateProjectView(project); err != nil {
		return Presentation{}, fmt.Errorf("modelingworkflow: Show returned an invalid created project: %w", err)
	}

	// Phase 5: expose the authoritative post-create status to the presenter.
	if next.Awaiting == AwaitingSource {
		return projectPresentation(
			StateNeedsInput,
			"The modeling project was created, but its first operation requires source material.",
			"Which workspace source should be used?",
			project,
		), nil
	}
	if project.Status == modelingapi.ProjectBlocked {
		if project.PublicError != nil {
			presentation := projectPresentation(
				StateFailed,
				"The modeling project was created but is blocked.",
				"",
				project,
			)
			failure := modelingapi.ClonePublicError(*project.PublicError)
			presentation.Failure = &failure
			return presentation, nil
		}
		return projectPresentation(
			StateNeedsInput,
			"The modeling project was created but needs additional information.",
			"What additional information should be provided?",
			project,
		), nil
	}
	return projectPresentation(
		stateFromProject(project),
		"The modeling project was created.",
		"",
		project,
	), nil
}

func collectStartInput(
	key BindingKey,
	current Binding,
	found bool,
	replace bool,
	intent Intent,
) (Binding, int, error) {
	// Build a candidate binding first; expected is the version used by CAS.
	expected := 0
	next := Binding{Key: key}
	if found {
		expected = current.Version
		if !replace {
			next = cloneBinding(current)
		}
	}
	next.Key = key
	next.Version = expected
	if replace {
		// StartNew intentionally clears the previous conversation project and inputs.
		next.ActiveProjectID = ""
		next.Title = ""
		next.Instruction = ""
		next.Sources = nil
		next.Awaiting = AwaitingNone
	}

	if title := strings.TrimSpace(intent.Title); title != "" {
		next.Title = title
	}
	if instruction := strings.TrimSpace(intent.Instruction); instruction != "" {
		next.Instruction = instruction
	}
	if len(intent.Sources) > 0 {
		next.Sources = modelingapi.CloneSources(intent.Sources)
	}

	// Validate the normalized candidate before it can be persisted or sent to Service.
	if next.Title != "" {
		if err := modelingapi.ValidateTitle(next.Title); err != nil {
			return Binding{}, 0, err
		}
	}
	if err := modelingapi.ValidateInstruction(next.Instruction); err != nil {
		return Binding{}, 0, err
	}
	if err := modelingapi.ValidateSources(next.Sources); err != nil {
		return Binding{}, 0, err
	}
	return next, expected, nil
}

func missingRequirementQuestion(missingTitle, missingInstruction bool) string {
	switch {
	case missingTitle && missingInstruction:
		return "What should the project be called, and what QEMU component or behavior should it model?"
	case missingTitle:
		return "What should this modeling project be called?"
	default:
		return "What QEMU component or behavior should this project model?"
	}
}
