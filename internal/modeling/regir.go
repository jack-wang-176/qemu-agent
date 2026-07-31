package modeling

// regir.go is the register intermediate representation: the one structured value
// the whole pipeline is organized around.
//
// It exists because the three model-facing stages must not hand each other free
// text. Extract produces a RegIR, Infer refines the same RegIR, and Emit renders
// code from it — so every claim about hardware that reaches generated C has
// passed Validate() first. A model that "almost" describes a device is a
// schema_invalid failure, not a device with a plausible register map.
//
// The rule Validate encodes is: reject, never repair. A misaligned offset or an
// overlapping register is a misread datasheet, and silently fixing it would
// produce code that compiles and lies. The only thing this file trims rather
// than refuses is prose length (Notes, Effect), because a long note is a
// cosmetic problem while a wrong offset is a functional one.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Access is the closed set of register access modes. It is a string type rather
// than an int so a decoded JSON value is legible in an artifact a human reads.
type Access string

const (
	AccessRO   Access = "ro"   // reads return state, writes are ignored
	AccessWO   Access = "wo"   // writes act, reads are undefined
	AccessRW   Access = "rw"   // plain storage or storage plus a side effect
	AccessW1C  Access = "w1c"  // write-one-to-clear, the classic interrupt-status mode
	AccessRsvd Access = "rsvd" // present in the map, deliberately unimplemented
)

// BusKind is where the device attaches. Only the buses the emit stage can
// actually render are legal: a RegIR that names a bus nothing can generate would
// pass extract and fail four stages later, which is the worst place to find out.
type BusKind string

const (
	BusSysbus BusKind = "sysbus"
	BusPCI    BusKind = "pci"
)

// Limits on the IR. They are constants rather than configuration because they
// bound what a *model* may claim, not what an operator may want: a 4096-register
// device is a hallucination or a parsing accident in every real datasheet.
const (
	maxRegisters   = 512
	maxFields      = 64
	maxNotes       = 64
	maxNoteBytes   = 2048
	maxEffectBytes = 2048
	maxIRQs        = 32
)

// devicePattern is the same shape the verify stage uses to build a qtest target
// and the emit stage uses to name files. Keeping it strict here means no later
// stage has to sanitize a device name it received from a model.
var devicePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// symbolPattern bounds register and field names. They become C identifiers and
// comment text, so anything that could close a comment or open a directive is
// refused rather than escaped.
var symbolPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// Field is one bit range inside a register.
type Field struct {
	Name  string `json:"name"`
	Bit   int    `json:"bit"`   // lowest bit of the range
	Width int    `json:"width"` // number of bits, >= 1
	// Description is prose for the generated comment. It is bounded, never
	// escaped: it is written into an artifact, not into a prompt.
	Description string `json:"description,omitempty"`
}

// Register is one addressable location.
//
// Reset is a pointer on purpose. "The datasheet does not say" and "the reset
// value is zero" are different facts, and collapsing them is exactly the silent
// default the pipeline is built to avoid: a nil Reset becomes an open question,
// while a zero Reset is a claim the generated code will encode.
type Register struct {
	Name   string  `json:"name"`
	Offset uint64  `json:"offset"`
	Width  int     `json:"width"` // bits: 8, 16, 32 or 64
	Access Access  `json:"access"`
	Reset  *uint64 `json:"reset,omitempty"`
	Fields []Field `json:"fields,omitempty"`
	Effect string  `json:"effect,omitempty"` // one sentence about the write side effect
}

// IRQ is one output line. Index is the sysbus IRQ number the emit stage wires.
type IRQ struct {
	Name        string `json:"name"`
	Index       int    `json:"index"`
	Description string `json:"description,omitempty"`
}

// RegIR is the whole device as the pipeline understands it.
//
// Notes is a first-class field rather than a comment because "what we do not
// know" is an output of this pipeline: it is what /modeling show surfaces and
// what open-questions.md is rendered from. A RegIR with no notes is a claim that
// the datasheet was fully understood.
type RegIR struct {
	Device     string     `json:"device"`
	BusKind    BusKind    `json:"bus_kind"`
	MMIOSize   uint64     `json:"mmio_size"`
	Registers  []Register `json:"registers"`
	Interrupts []IRQ      `json:"interrupts,omitempty"`
	Notes      []string   `json:"notes,omitempty"`
}

// Validate is the single gate between model output and everything downstream.
// Every caller — extract, infer, emit — runs it, and a failure is wrapped as
// ErrSchemaInvalid so the pipeline reports "schema_invalid" without quoting the
// datasheet line that caused it.
//
// The checks run cheapest-first and in dependency order: identity, then the
// address space, then each register on its own, then the relationships between
// registers. Later checks assume the earlier ones passed.
func (r RegIR) Validate() error {
	// 1: identity. The device name reaches a file name, a C symbol and a qtest
	// argument, so it is the strictest field in the IR.
	if !devicePattern.MatchString(r.Device) {
		return fmt.Errorf("%w: device name %q is not a lowercase identifier", ErrSchemaInvalid, r.Device)
	}
	switch r.BusKind {
	case BusSysbus, BusPCI:
	default:
		return fmt.Errorf("%w: unsupported bus kind %q", ErrSchemaInvalid, r.BusKind)
	}

	// 2: the address space. A zero MMIOSize would make every offset check below
	// vacuous, and a non-power-of-two region cannot be mapped as one MemoryRegion.
	if r.MMIOSize == 0 || r.MMIOSize&(r.MMIOSize-1) != 0 {
		return fmt.Errorf("%w: mmio size %d must be a non-zero power of two", ErrSchemaInvalid, r.MMIOSize)
	}
	if len(r.Registers) == 0 {
		return fmt.Errorf("%w: device %q has no registers", ErrSchemaInvalid, r.Device)
	}
	if len(r.Registers) > maxRegisters {
		return fmt.Errorf("%w: device %q has %d registers, limit is %d", ErrSchemaInvalid, r.Device, len(r.Registers), maxRegisters)
	}

	// 3: each register on its own, plus the two uniqueness maps. Names and offsets
	// are collected here so the overlap pass below does not have to re-walk.
	names := make(map[string]struct{}, len(r.Registers))
	spans := make([]span, 0, len(r.Registers))
	for _, register := range r.Registers {
		if err := register.validate(r.MMIOSize); err != nil {
			return err
		}
		key := strings.ToUpper(register.Name)
		if _, duplicate := names[key]; duplicate {
			// Case-insensitive, because the names become C macros: CTRL and Ctrl
			// would collide only after code generation.
			return fmt.Errorf("%w: register name %q appears twice", ErrSchemaInvalid, register.Name)
		}
		names[key] = struct{}{}
		spans = append(spans, span{start: register.Offset, end: register.Offset + uint64(register.Width/8), name: register.Name})
	}

	// 4: relationships. Overlap is checked by sorting rather than pairwise, so a
	// 512-register map costs one sort instead of 130k comparisons.
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for index := 1; index < len(spans); index++ {
		if spans[index].start < spans[index-1].end {
			return fmt.Errorf("%w: registers %q and %q overlap", ErrSchemaInvalid, spans[index-1].name, spans[index].name)
		}
	}

	// 5: interrupts. Duplicate indices are refused because the emit stage turns
	// each one into a distinct sysbus IRQ, and two lines on one index is a map
	// nobody can wire.
	if len(r.Interrupts) > maxIRQs {
		return fmt.Errorf("%w: device %q declares %d interrupts, limit is %d", ErrSchemaInvalid, r.Device, len(r.Interrupts), maxIRQs)
	}
	indices := make(map[int]struct{}, len(r.Interrupts))
	for _, irq := range r.Interrupts {
		if !symbolPattern.MatchString(irq.Name) {
			return fmt.Errorf("%w: interrupt name %q is not an identifier", ErrSchemaInvalid, irq.Name)
		}
		if irq.Index < 0 || irq.Index >= maxIRQs {
			return fmt.Errorf("%w: interrupt %q has index %d", ErrSchemaInvalid, irq.Name, irq.Index)
		}
		if _, duplicate := indices[irq.Index]; duplicate {
			return fmt.Errorf("%w: interrupt index %d is claimed twice", ErrSchemaInvalid, irq.Index)
		}
		indices[irq.Index] = struct{}{}
	}
	if len(r.Notes) > maxNotes {
		return fmt.Errorf("%w: device %q carries %d notes, limit is %d", ErrSchemaInvalid, r.Device, len(r.Notes), maxNotes)
	}
	return nil
}

// span is the half-open byte range one register occupies. It exists only for the
// overlap pass.
type span struct {
	start, end uint64
	name       string
}

// validate checks one register against the region it lives in. It is a method so
// the rules are stated once and shared by Validate and by infer's merge, which
// re-validates every register it touched.
func (reg Register) validate(mmioSize uint64) error {
	if !symbolPattern.MatchString(reg.Name) {
		return fmt.Errorf("%w: register name %q is not an identifier", ErrSchemaInvalid, reg.Name)
	}
	switch reg.Width {
	case 8, 16, 32, 64:
	default:
		return fmt.Errorf("%w: register %q has width %d", ErrSchemaInvalid, reg.Name, reg.Width)
	}
	switch reg.Access {
	case AccessRO, AccessWO, AccessRW, AccessW1C, AccessRsvd:
	default:
		return fmt.Errorf("%w: register %q has access %q", ErrSchemaInvalid, reg.Name, reg.Access)
	}
	// Alignment is a hardware fact, not a style rule: an unaligned MMIO register
	// would be split across two accesses by the memory API.
	size := uint64(reg.Width / 8)
	if reg.Offset%size != 0 {
		return fmt.Errorf("%w: register %q at offset %#x is not %d-byte aligned", ErrSchemaInvalid, reg.Name, reg.Offset, size)
	}
	if reg.Offset > mmioSize-size {
		// Written as a subtraction rather than Offset+size > mmioSize so a huge
		// offset cannot wrap around and pass the check.
		return fmt.Errorf("%w: register %q at offset %#x does not fit in %#x bytes", ErrSchemaInvalid, reg.Name, reg.Offset, mmioSize)
	}
	if reg.Reset != nil && reg.Width < 64 {
		// A reset value wider than its register means the model merged two rows of
		// the datasheet; the generated code would silently truncate it.
		if limit := uint64(1)<<uint(reg.Width) - 1; *reg.Reset > limit {
			return fmt.Errorf("%w: register %q reset %#x exceeds its %d-bit width", ErrSchemaInvalid, reg.Name, *reg.Reset, reg.Width)
		}
	}
	return reg.validateFields()
}

// validateFields checks the bit ranges inside one register. Same shape as the
// register overlap pass: sort, then compare neighbours.
func (reg Register) validateFields() error {
	if len(reg.Fields) == 0 {
		return nil
	}
	if len(reg.Fields) > maxFields {
		return fmt.Errorf("%w: register %q has %d fields, limit is %d", ErrSchemaInvalid, reg.Name, len(reg.Fields), maxFields)
	}
	names := make(map[string]struct{}, len(reg.Fields))
	ranges := make([]span, 0, len(reg.Fields))
	for _, field := range reg.Fields {
		if !symbolPattern.MatchString(field.Name) {
			return fmt.Errorf("%w: field %q of %q is not an identifier", ErrSchemaInvalid, field.Name, reg.Name)
		}
		if field.Width < 1 || field.Bit < 0 || field.Bit+field.Width > reg.Width {
			return fmt.Errorf("%w: field %q of %q spans bits %d..%d outside a %d-bit register",
				ErrSchemaInvalid, field.Name, reg.Name, field.Bit, field.Bit+field.Width-1, reg.Width)
		}
		key := strings.ToUpper(field.Name)
		if _, duplicate := names[key]; duplicate {
			return fmt.Errorf("%w: field %q of %q appears twice", ErrSchemaInvalid, field.Name, reg.Name)
		}
		names[key] = struct{}{}
		ranges = append(ranges, span{start: uint64(field.Bit), end: uint64(field.Bit + field.Width), name: field.Name})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	for index := 1; index < len(ranges); index++ {
		if ranges[index].start < ranges[index-1].end {
			return fmt.Errorf("%w: fields %q and %q of %q overlap", ErrSchemaInvalid, ranges[index-1].name, ranges[index].name, reg.Name)
		}
	}
	return nil
}

// Normalize is the one operation allowed to change a decoded IR, and it changes
// only prose. It runs *before* Validate so a datasheet paragraph pasted into
// Effect becomes a bounded sentence plus a note, rather than a rejected device:
// a long description is a cosmetic problem, and refusing the whole extraction
// over it would waste a model call that got the hardware right.
//
// It returns the notes it added rather than only mutating, so the caller can put
// them in open-questions.md as well.
func (r *RegIR) Normalize() []string {
	added := make([]string, 0, 4)
	r.Device = strings.ToLower(strings.TrimSpace(r.Device))
	r.BusKind = BusKind(strings.ToLower(strings.TrimSpace(string(r.BusKind))))
	for index := range r.Registers {
		register := &r.Registers[index]
		register.Name = strings.TrimSpace(register.Name)
		register.Access = Access(strings.ToLower(strings.TrimSpace(string(register.Access))))
		if trimmed, cut := clampText(register.Effect, maxEffectBytes); cut {
			register.Effect = trimmed
			added = append(added, fmt.Sprintf("effect text of register %s was truncated to %d bytes", register.Name, maxEffectBytes))
		} else {
			register.Effect = trimmed
		}
		for fieldIndex := range register.Fields {
			field := &register.Fields[fieldIndex]
			field.Name = strings.TrimSpace(field.Name)
			field.Description, _ = clampText(field.Description, maxNoteBytes)
		}
	}
	for index := range r.Interrupts {
		r.Interrupts[index].Name = strings.TrimSpace(r.Interrupts[index].Name)
		r.Interrupts[index].Description, _ = clampText(r.Interrupts[index].Description, maxNoteBytes)
	}
	notes := make([]string, 0, len(r.Notes)+len(added))
	for _, note := range r.Notes {
		trimmed, _ := clampText(note, maxNoteBytes)
		if trimmed != "" {
			notes = append(notes, trimmed)
		}
	}
	r.Notes = append(notes, added...)
	if len(r.Notes) > maxNotes {
		// Dropping the tail is safe because notes are already ordered
		// most-specific-first by the stages; the fact that some were dropped is
		// itself recorded as the last note.
		r.Notes = append(r.Notes[:maxNotes-1], fmt.Sprintf("%d further notes were dropped at the %d-note limit", len(r.Notes)-maxNotes+1, maxNotes))
	}
	return added
}

// OpenQuestions lists everything the IR admits it does not know. It is derived
// rather than stored so it cannot disagree with the IR: a register that gains a
// reset value stops being an open question the moment the IR changes.
func (r RegIR) OpenQuestions() []string {
	questions := make([]string, 0, len(r.Registers)+len(r.Notes))
	for _, register := range r.Registers {
		if register.Reset == nil && register.Access != AccessWO && register.Access != AccessRsvd {
			// A missing reset value is the one gap that must never be defaulted:
			// "unknown" and "zero" produce different, both-plausible devices.
			questions = append(questions, fmt.Sprintf("register %s has no documented reset value", register.Name))
		}
		if register.Access == AccessRW && register.Effect == "" && len(register.Fields) == 0 {
			questions = append(questions, fmt.Sprintf("register %s is read/write with no documented fields or side effect", register.Name))
		}
	}
	if len(r.Interrupts) == 0 {
		questions = append(questions, "no interrupt lines were identified; confirm the device is polled")
	}
	questions = append(questions, r.Notes...)
	return questions
}

// clampText trims and bounds one prose value. The bound is on bytes rather than
// runes because it protects an artifact size limit, and it cuts on a rune
// boundary so the result stays valid UTF-8.
func clampText(raw string, limit int) (string, bool) {
	collapsed := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if len(collapsed) <= limit {
		return collapsed, false
	}
	cut := collapsed[:limit]
	for len(cut) > 0 && !isUTF8Boundary(collapsed, len(cut)) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut) + "…", true
}

// isUTF8Boundary reports whether index starts a new rune.
func isUTF8Boundary(value string, index int) bool {
	if index >= len(value) {
		return true
	}
	return value[index]&0xC0 != 0x80
}
