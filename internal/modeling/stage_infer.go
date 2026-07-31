package modeling

// stage_infer.go is the third stage: it fills the *behavioural* gaps the extract
// stage left — reset values, write side effects, bit fields, interrupts — without
// being allowed to change the register map itself.
//
// That restriction is the entire reason this stage exists as a separate step. A
// model asked to "improve the register map" will move an offset every time it
// runs, and a project whose addresses change per run cannot be reviewed. So infer
// merges instead of replacing: the offsets, widths and names that extract read out
// of a datasheet are facts, and a proposal that contradicts one is rejected
// item-by-item with a note explaining what was dropped. The worst outcome of a
// confused model is therefore a no-op plus a few notes, never a silently rewritten
// address map.
//
// The stage has no tools: its only input is this project's own reg-ir.json, read
// through the Pipeline's Open function, and its only side effect is one model call.

import (
	"context"
	"fmt"
	"strings"
)

// inferStage implements StageRunner for StageInfer.
type inferStage struct{}

var _ StageRunner = inferStage{}

func NewInferStage() StageRunner { return inferStage{} }

func (inferStage) Name() Stage { return StageInfer }

// inferSystemPrompt asks for the same schema the extract stage uses, because the
// merge below compares the reply field by field against the stored IR — a second,
// "patch-shaped" schema would need its own validator and would let the model
// address a register that does not exist.
//
// The prompt states the merge rules explicitly even though they are enforced in
// code. A model that knows offsets will be discarded spends its answer on the
// parts that are actually used.
const inferSystemPrompt = `You complete the behavioural description of a hardware register map.

You are given a register map that was extracted from documentation. Answer with
one JSON object in exactly the same shape, and nothing else. No prose, no code
fence.

What you may add:
- "reset": the documented reset value of a register, only when it is stated.
- "effect": one sentence on what a write to the register does.
- "fields": bit fields inside a register, as {"name","bit","width","description"}.
- "interrupts": interrupt lines the device raises, as {"name","index","description"}.
- "notes": anything you are unsure about, in your own words.

What you must not change:
- "device", "bus_kind", "mmio_size".
- Any register's "name", "offset" or "width". Repeat them exactly as given.
  A register whose offset or width differs from the input is discarded whole.
- Do not add or remove registers. Report a register you believe is missing as a note.

Rules:
- Omit "reset" when the documentation does not state one. Never write 0 to mean
  "unknown" — an unknown reset value is a note, not a zero.
- "access" values are "ro", "wo", "rw", "w1c", "rsvd". Keep the given value unless
  the input is clearly wrong, and then say so in a note.
- Field bit ranges must fit inside the register width and must not overlap.`

// Run reads the stored IR, asks for one completed copy, merges it under the
// no-rewrite rule and commits the result. The steps are numbered because the merge
// makes this the second-longest body in the package.
func (s inferStage) Run(ctx context.Context, in StageInput) (StageOutput, error) {
	// 1: the IR is the only input. loadRegIR re-validates it, so an IR written by
	// an older binary fails here rather than reaching the code generator.
	base, _, err := loadRegIR(in)
	if err != nil {
		return StageOutput{}, err
	}

	// 2: build the prompt. The IR is this project's own artifact, but it is model
	// output about a third-party document, so it goes in as a labelled data block
	// exactly like the datasheet did.
	body, err := encodeRegIR(base)
	if err != nil {
		return StageOutput{}, err
	}
	sections := []string{untrustedBlock("extracted register map", string(body))}
	if planRef, ok := findStageArtifact(in.Inputs, KindPlan); ok {
		planBody, planErr := readArtifact(in, planRef)
		if planErr != nil {
			return StageOutput{}, planErr
		}
		sections = append(sections, untrustedBlock("modeling plan", string(planBody)))
	}
	if request := strings.TrimSpace(in.Request); request != "" {
		sections = append(sections, untrustedBlock("additional instructions from the user", request))
	}

	// 3: one model call.
	reply, err := complete(ctx, in, inferSystemPrompt, strings.Join(sections, "\n\n"))
	if err != nil {
		return StageOutput{}, err
	}

	// 4: decode strictly. As in extract, a reply that does not decode is a
	// schema_invalid failure whose raw text is kept as evidence and nowhere else.
	var proposal RegIR
	if err := decodeStrict(reply, &proposal); err != nil {
		return StageOutput{Evidence: []Draft{rawDraft(StageInfer, reply)}}, err
	}
	proposal.Normalize()

	// 5: merge. mergeInferred never returns an error: every disagreement becomes a
	// dropped item plus a note, so a hostile or confused reply degrades to "nothing
	// was learned" instead of failing a stage that already has a usable IR.
	merged, notes := mergeInferred(base, proposal)
	merged.Notes = append(merged.Notes, notes...)
	merged.Normalize()

	// 6: validate the merge, not the reply. The base was already valid, so a failure
	// here means the merge accepted something it should not have — fields that
	// overlap is the realistic case — and the raw reply is kept for diagnosis.
	if err := merged.Validate(); err != nil {
		return StageOutput{Evidence: []Draft{rawDraft(StageInfer, reply)}}, err
	}
	mergedBody, err := encodeRegIR(merged)
	if err != nil {
		return StageOutput{}, err
	}

	// 7: commit the refreshed IR and the questions that remain. open-questions.md is
	// rewritten here rather than edited, so it always describes the IR next to it.
	questions := merged.OpenQuestions()
	artifacts := []Draft{{Stage: StageInfer, Name: artifactRegIR, Kind: KindRegIR, Body: mergedBody}}
	if len(questions) > 0 {
		artifacts = append(artifacts, questionsDraft(StageInfer, merged.Device, questions))
	}
	return StageOutput{
		Artifacts: artifacts,
		Summary: fmt.Sprintf("completed behaviour for %s: %d reset value(s), %d effect(s), %d field set(s), %d interrupt(s); %d merge note(s); %d open question(s)",
			merged.Device, countResets(merged)-countResets(base), countEffects(merged)-countEffects(base),
			countFieldSets(merged)-countFieldSets(base), len(merged.Interrupts), len(notes), len(questions)),
	}, nil
}

// mergeInferred is the no-rewrite rule in code. It returns the merged IR and the
// notes describing everything it refused, and it is deliberately total: there is
// no input for which it fails, because the base IR is already a valid answer.
//
// The shape is "copy the base, then fill holes". A value is only ever written when
// the base has none, so re-running infer on a completed IR is a no-op — which is
// what makes the stage safe to retry.
func mergeInferred(base, proposal RegIR) (RegIR, []string) {
	merged := base
	merged.Registers = append([]Register(nil), base.Registers...)
	notes := make([]string, 0, 4)

	// Identity first. The device name and MMIO window are extract's findings; a
	// proposal that disagrees is reporting a different device, so nothing from it
	// can be trusted to belong here — but the note is worth keeping.
	if proposal.Device != "" && proposal.Device != base.Device {
		notes = append(notes, fmt.Sprintf("infer proposed a different device name (%s); kept %s",
			safeSymbol(proposal.Device), base.Device))
	}
	if proposal.BusKind != "" && proposal.BusKind != base.BusKind {
		notes = append(notes, fmt.Sprintf("infer proposed bus %s; kept %s", safeSymbol(string(proposal.BusKind)), base.BusKind))
	}
	if proposal.MMIOSize != 0 && proposal.MMIOSize != base.MMIOSize {
		notes = append(notes, fmt.Sprintf("infer proposed mmio_size %d; kept %d", proposal.MMIOSize, base.MMIOSize))
	}

	// Index the base by lowercased name: Validate already guarantees those keys are
	// unique, so this cannot collapse two registers into one.
	index := make(map[string]int, len(merged.Registers))
	for position, register := range merged.Registers {
		index[strings.ToLower(register.Name)] = position
	}

	for _, candidate := range proposal.Registers {
		position, ok := index[strings.ToLower(candidate.Name)]
		if !ok {
			// A register the extraction did not see is a claim about the hardware, not
			// a gap to fill. It is recorded so a human can go back to the datasheet.
			notes = append(notes, fmt.Sprintf("infer proposed a register %s that extract did not find; ignored",
				safeSymbol(candidate.Name)))
			continue
		}
		target := &merged.Registers[position]
		// The three facts. Any mismatch discards the whole item: a model that got
		// the offset wrong has no credibility about that register's fields either.
		if candidate.Offset != target.Offset || candidate.Width != target.Width {
			notes = append(notes, fmt.Sprintf("infer changed %s (offset/width); the extracted values were kept and its other suggestions dropped",
				target.Name))
			continue
		}
		notes = append(notes, mergeRegister(target, candidate)...)
	}

	// Interrupts are a list, not a map keyed by anything extract owns, so they are
	// taken wholesale only when there are none — otherwise only descriptions are
	// filled in, by matching index.
	notes = append(notes, mergeInterrupts(&merged, proposal.Interrupts)...)

	// The model's own uncertainty is the point of invariant 5: a doubt it wrote down
	// must reach open-questions.md rather than be resolved by a default. The notes
	// are prefixed so a reviewer can tell them from extract's, and clamped by
	// Normalize afterwards.
	for _, note := range proposal.Notes {
		if trimmed := strings.TrimSpace(note); trimmed != "" {
			notes = append(notes, "infer: "+trimmed)
		}
	}
	return merged, notes
}

// mergeRegister fills one register's holes. Each branch is guarded by "the base has
// nothing here", which is the difference between completing an extraction and
// letting a model overwrite it.
func mergeRegister(target *Register, candidate Register) []string {
	notes := make([]string, 0, 2)
	if candidate.Access != "" && candidate.Access != target.Access {
		// Access is extract's finding too, but unlike an offset a wrong one is
		// visible in review, so it is reported rather than silently ignored.
		notes = append(notes, fmt.Sprintf("infer proposed access %q for %s; kept %q",
			safeSymbol(string(candidate.Access)), target.Name, target.Access))
	}
	if target.Reset == nil && candidate.Reset != nil {
		if resetFitsWidth(*candidate.Reset, target.Width) {
			value := *candidate.Reset
			target.Reset = &value
		} else {
			notes = append(notes, fmt.Sprintf("infer proposed a reset value for %s that does not fit %d bits; left unknown",
				target.Name, target.Width))
		}
	} else if target.Reset != nil && candidate.Reset != nil && *candidate.Reset != *target.Reset {
		notes = append(notes, fmt.Sprintf("infer proposed a different reset value for %s; kept the extracted one", target.Name))
	}
	if strings.TrimSpace(target.Effect) == "" && strings.TrimSpace(candidate.Effect) != "" {
		target.Effect = candidate.Effect
	}
	if len(target.Fields) == 0 && len(candidate.Fields) > 0 {
		// Field layouts are accepted as a block and then judged by Validate: a
		// half-merged bit layout would be worse than none.
		target.Fields = append([]Field(nil), candidate.Fields...)
		return notes
	}
	// The base already documents fields, so only empty descriptions are filled.
	// Names, bits and widths stay as extracted for the same reason offsets do.
	byName := make(map[string]int, len(target.Fields))
	for position, field := range target.Fields {
		byName[strings.ToLower(field.Name)] = position
	}
	for _, field := range candidate.Fields {
		position, ok := byName[strings.ToLower(field.Name)]
		if !ok {
			continue
		}
		if strings.TrimSpace(target.Fields[position].Description) == "" {
			target.Fields[position].Description = field.Description
		}
	}
	return notes
}

// mergeInterrupts is separate because the two cases are not variations of one
// rule: an empty interrupt list is a gap infer is expected to fill, while a
// non-empty one is extract's finding and only its prose may be completed.
func mergeInterrupts(merged *RegIR, proposed []IRQ) []string {
	if len(proposed) == 0 {
		return nil
	}
	if len(merged.Interrupts) == 0 {
		merged.Interrupts = append([]IRQ(nil), proposed...)
		return nil
	}
	notes := make([]string, 0, 1)
	byIndex := make(map[int]int, len(merged.Interrupts))
	for position, irq := range merged.Interrupts {
		byIndex[irq.Index] = position
	}
	for _, irq := range proposed {
		position, ok := byIndex[irq.Index]
		if !ok {
			notes = append(notes, fmt.Sprintf("infer proposed an extra interrupt at index %d; ignored", irq.Index))
			continue
		}
		if strings.TrimSpace(merged.Interrupts[position].Description) == "" {
			merged.Interrupts[position].Description = irq.Description
		}
	}
	return notes
}

// resetFitsWidth repeats the width check Validate performs, because the merge has
// to decide *whether to take a value* before the IR is validated as a whole. If it
// did not, one oversized reset value would fail the entire stage instead of
// becoming a note.
func resetFitsWidth(reset uint64, width int) bool {
	if width >= 64 {
		return true
	}
	return reset>>uint(width) == 0
}

// safeSymbol bounds a model-supplied identifier before it is quoted in a note.
// Notes end up inside reg-ir.json, which is untrusted content by design — but they
// are also read back by the emit stage's templates, so an unbounded or
// non-identifier string is replaced rather than passed on.
func safeSymbol(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !symbolPattern.MatchString(trimmed) {
		return "<unnamed>"
	}
	bounded, _ := clampText(trimmed, 64)
	return bounded
}

// countResets, countEffects and countFieldSets exist only to make the stage
// summary honest: "completed behaviour" without a count is unverifiable, and
// diffing the counts before and after the merge is the cheapest way to say how
// much was actually learned.
func countResets(ir RegIR) int {
	total := 0
	for _, register := range ir.Registers {
		if register.Reset != nil {
			total++
		}
	}
	return total
}

func countEffects(ir RegIR) int {
	total := 0
	for _, register := range ir.Registers {
		if strings.TrimSpace(register.Effect) != "" {
			total++
		}
	}
	return total
}

func countFieldSets(ir RegIR) int {
	total := 0
	for _, register := range ir.Registers {
		if len(register.Fields) > 0 {
			total++
		}
	}
	return total
}
