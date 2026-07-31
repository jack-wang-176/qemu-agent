package modeling

// stage_plan.go is the first stage: it turns a human request plus a project
// title into a written modeling plan.
//
// Plan is the cheapest stage and the only one whose output is prose, and that is
// deliberate. Everything after it is structured and checkable, so the place to
// argue about *what* to build is here, before a register map exists. A reviewer
// who disagrees with plan.md has lost one model call; a reviewer who disagrees
// with device.c has lost four stages.
//
// The stage has no tools and no filesystem access: it is given the request text
// as data and returns one draft. Its whole side-effect surface is a single
// Completer call.

import (
	"context"
	"fmt"
	"strings"
)

// planStage implements StageRunner for StagePlan. It is a struct with no fields
// rather than a function because StageRunner has to name its stage, and a
// registry of typed values is what makes NewPipeline able to reject duplicates.
type planStage struct{}

var _ StageRunner = planStage{}

// NewPlanStage is the constructor the build layer calls. Stages are constructed,
// not referenced as package-level values, so a future stage that needs a
// dependency does not change how the others are wired.
func NewPlanStage() StageRunner { return planStage{} }

func (planStage) Name() Stage { return StagePlan }

// planSystemPrompt states the contract with the model. It is a constant, not a
// template: the only thing that varies between projects is the data block below
// it, so a prompt injected through a project title cannot change the
// instructions.
const planSystemPrompt = `You are a QEMU device modeling engineer writing an implementation plan.

Write Markdown with exactly these sections:

## Device
One paragraph: what the device is and which QEMU bus it belongs on (sysbus or pci).

## Register map strategy
Where the register definitions will come from (datasheet section, vendor header,
Linux driver) and which ones matter for a working model.

## Behaviour
Which registers have side effects, how interrupts are raised and cleared, and
what a minimal model may leave unimplemented.

## Open questions
Everything the request does not answer. Be explicit; an empty section is a claim
that nothing is unknown.

Rules:
- Do not invent register offsets or reset values. This stage plans; a later stage
  extracts them from a datasheet.
- Do not write C code.
- Keep it under 400 lines.`

// Run makes one model call and returns one draft.
//
// The request text is untrusted input — it arrives from a chat message — so it is
// passed inside a labelled data block rather than concatenated into the
// instructions. That is the same rule the prompt overlay applies to memory: the
// model is told to describe the block, never to obey it.
func (s planStage) Run(ctx context.Context, in StageInput) (StageOutput, error) {
	request := strings.TrimSpace(in.Request)
	if request == "" {
		// The plan stage is the only one that has nothing to work from without a
		// request, so this is a usable failure rather than a guess.
		return StageOutput{}, fmt.Errorf("%w: the plan stage needs a device request", ErrStageUnavailable)
	}
	user := strings.Join([]string{
		untrustedBlock("project title", in.Project.Title),
		untrustedBlock("device request", request),
		sourceHints(in.Sources),
	}, "\n\n")

	reply, err := complete(ctx, in, planSystemPrompt, user)
	if err != nil {
		return StageOutput{}, err
	}
	// The reply is prose, so there is nothing to decode — but it is still model
	// output about untrusted input, which is why it goes to an artifact and never
	// into the project record or a log line.
	body := strings.TrimSpace(reply) + "\n"
	return StageOutput{
		Artifacts: []Draft{{Stage: StagePlan, Name: artifactPlan, Kind: KindPlan, Body: []byte(body)}},
		Summary:   fmt.Sprintf("wrote %s (%d bytes); review it before extracting the register map", artifactPlan, len(body)),
	}, nil
}

// sourceHints tells the plan which files the user named without reading them. The
// plan stage has no read tool on purpose — a plan that quoted a datasheet would
// be doing the extract stage's job — so the paths are listed as context only.
func sourceHints(sources []string) string {
	if len(sources) == 0 {
		return "The user named no datasheet or header yet; say so in the open questions."
	}
	quoted := make([]string, 0, len(sources))
	for _, source := range sources {
		trimmed, _ := clampText(source, 256)
		quoted = append(quoted, "- "+trimmed)
	}
	return "The user named these sources for the extract stage (their contents are not available yet):\n" + strings.Join(quoted, "\n")
}
