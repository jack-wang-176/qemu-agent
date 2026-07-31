package modeling

// stage_extract.go is the second stage: it reads the sources a human named and
// produces the register IR.
//
// This is the stage where untrusted third-party content enters the pipeline, so
// two rules are absolute here. First, the stage does not open a file — it calls
// the audited `read` tool, which means the security policy decides what is
// readable and the audit log records exactly which files were consulted. Second,
// the model's answer is decoded strictly and validated as a RegIR before it is
// persisted; a reply that does not decode is a schema_invalid failure whose raw
// text is kept as evidence and nowhere else.

import (
	"context"
	"fmt"
	"strings"
)

// extractStage implements StageRunner for StageExtract.
type extractStage struct{}

var _ StageRunner = extractStage{}

func NewExtractStage() StageRunner { return extractStage{} }

func (extractStage) Name() Stage { return StageExtract }

// extractSystemPrompt is the schema contract. The example is spelled out because
// a structured answer from an unstructured document is the hardest thing this
// pipeline asks of a model, and the failure mode it must avoid — inventing an
// offset that "looks right" — is stated as a rule rather than implied.
const extractSystemPrompt = `You extract a register map from hardware documentation.

Answer with one JSON object and nothing else. No prose, no code fence.

{
  "device": "acme_uart",
  "bus_kind": "sysbus",
  "mmio_size": 4096,
  "registers": [
    {
      "name": "CTRL",
      "offset": 0,
      "width": 32,
      "access": "rw",
      "reset": 0,
      "fields": [{"name": "ENABLE", "bit": 0, "width": 1, "description": "enables the device"}],
      "effect": "writing ENABLE starts the transmitter"
    }
  ],
  "interrupts": [{"name": "irq", "index": 0, "description": "raised on receive"}],
  "notes": ["the datasheet does not document offsets 0x40..0x4c"]
}

Rules:
- "device" is a lowercase C identifier. "bus_kind" is "sysbus" or "pci".
- "mmio_size" is a power of two large enough for every register.
- "access" is one of "ro", "wo", "rw", "w1c", "rsvd".
- "width" is 8, 16, 32 or 64. Offsets must be aligned to width/8 and must not overlap.
- Omit "reset" entirely when the documentation does not state a reset value. Do
  not write 0 to mean "unknown".
- Every gap, contradiction or guess goes into "notes". Notes are expected; an
  empty list claims the document was complete.
- Only report registers the documentation actually describes.`

// Run reads each source through the tool, asks for one JSON object, and commits
// the IR. It is the longest stage body in the package, so the steps are numbered.
func (s extractStage) Run(ctx context.Context, in StageInput) (StageOutput, error) {
	// 1: this stage cannot work without a source. Refusing here rather than asking
	// the model to invent a register map is the whole point of the stage.
	if len(in.Sources) == 0 {
		return StageOutput{}, fmt.Errorf("%w: extract needs at least one --source=<path> to read", ErrStageUnavailable)
	}

	// 2: read the sources through the audited tool. Failures are per-source and
	// fatal: an extraction that silently skipped half the datasheet would produce
	// a register map that looks complete.
	excerpts := make([]string, 0, len(in.Sources))
	for _, source := range in.Sources {
		body, err := readSource(ctx, in, source)
		if err != nil {
			return StageOutput{}, err
		}
		excerpts = append(excerpts, untrustedBlock("source "+source, body))
	}

	// 3: the plan, if there is one, goes in as context. It is this project's own
	// earlier artifact, so it is model output about untrusted input — still a data
	// block, not an instruction.
	sections := make([]string, 0, len(excerpts)+2)
	if planRef, ok := findStageArtifact(in.Inputs, KindPlan); ok {
		planBody, err := readArtifact(in, planRef)
		if err != nil {
			return StageOutput{}, err
		}
		sections = append(sections, untrustedBlock("modeling plan", string(planBody)))
	}
	sections = append(sections, excerpts...)
	if request := strings.TrimSpace(in.Request); request != "" {
		sections = append(sections, untrustedBlock("additional instructions from the user", request))
	}

	// 4: one model call.
	reply, err := complete(ctx, in, extractSystemPrompt, strings.Join(sections, "\n\n"))
	if err != nil {
		return StageOutput{}, err
	}

	// 5: decode and validate. A failure returns the raw reply as an evidence draft
	// *alongside* the error: the Pipeline commits the evidence of a failed stage so
	// that a schema_invalid project has something a human can read, while the
	// project record still holds nothing but the category.
	ir, err := decodeAndValidate(reply)
	if err != nil {
		return StageOutput{Evidence: []Draft{rawDraft(StageExtract, reply)}}, err
	}

	// 6: commit the IR plus the questions it raised. open-questions.md is written
	// here as well as by infer, because a datasheet that documents nothing about
	// reset values must not look like a clean extraction.
	body, err := encodeRegIR(ir)
	if err != nil {
		return StageOutput{}, err
	}
	questions := ir.OpenQuestions()
	artifacts := []Draft{{Stage: StageExtract, Name: artifactRegIR, Kind: KindRegIR, Body: body}}
	if len(questions) > 0 {
		artifacts = append(artifacts, questionsDraft(StageExtract, ir.Device, questions))
	}
	return StageOutput{
		Artifacts: artifacts,
		Summary: fmt.Sprintf("extracted %d registers and %d interrupts for %s; %d open question(s)",
			len(ir.Registers), len(ir.Interrupts), ir.Device, len(questions)),
	}, nil
}

// readSource pulls one file in through the `read` tool.
//
// The path is passed to the tool exactly as the user typed it. That is not
// laziness: the tool resolves it against the workspace and refuses anything
// outside, so validating it here as well would create a second, weaker copy of a
// policy that already exists — and the audit entry would then be missing for the
// paths this layer rejected.
func readSource(ctx context.Context, in StageInput, source string) (string, error) {
	executor, err := requireExecutor(in)
	if err != nil {
		return "", err
	}
	result, err := executor.Run(ctx, "read", map[string]any{"file_path": source})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		// The tool's error may quote a resolved absolute path, so it is replaced by
		// a category. Which source failed is not secret — the user typed it — but it
		// is reported through the summary, not through a wrapped error.
		return "", fmt.Errorf("%w: reading a source was refused or failed", ErrToolDenied)
	}
	body := result.ModelOutput
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("%w: a source produced no content", ErrToolDenied)
	}
	return body, nil
}

// decodeAndValidate is the gate between a reply and an IR. Normalize runs before
// Validate so bounded prose problems are fixed and recorded as notes, while every
// structural claim is judged as written.
func decodeAndValidate(reply string) (RegIR, error) {
	var ir RegIR
	if err := decodeStrict(reply, &ir); err != nil {
		return RegIR{}, err
	}
	ir.Normalize()
	if err := ir.Validate(); err != nil {
		return RegIR{}, err
	}
	return ir, nil
}

// questionsDraft renders the open questions as Markdown. It is a separate
// artifact rather than a section of reg-ir.json so a reviewer can read it without
// reading JSON, and so it can be regenerated by infer without touching the IR.
func questionsDraft(stage Stage, device string, questions []string) Draft {
	lines := make([]string, 0, len(questions)+3)
	lines = append(lines, fmt.Sprintf("# Open questions for %s", device), "",
		"These are gaps the pipeline refuses to fill with defaults.", "")
	for _, question := range questions {
		bounded, _ := clampText(question, maxNoteBytes)
		lines = append(lines, "- "+bounded)
	}
	return Draft{
		Stage: stage, Name: artifactOpenQuestions, Kind: KindPlan,
		Body: []byte(strings.Join(lines, "\n") + "\n"),
	}
}
