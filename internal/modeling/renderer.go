package modeling

// renderer.go turns a validated register IR into QEMU C source. It is the only
// code generator in the project, and it is a *pure function*: no clock, no
// filesystem, no model call. Two consequences make the emit stage reviewable —
// the same IR always renders byte-identical output, so a re-run produces the same
// content address and `/modeling diff` is stable; and everything a reviewer reads
// in the diff was derived from the IR they already approved, never from a fresh
// model reply.
//
// What it does not do is claim the result compiles. A generated device is a
// skeleton whose register accessors are complete and whose behaviour is annotated
// with what the datasheet did not say; the verify stage is what turns that claim
// into evidence.

import (
	"sort"
	"strings"
)

// RenderedFile is one generated file, together with where it belongs in a QEMU
// tree. Name is the artifact name (flat, no separators, because the artifact store
// refuses paths); Path is the target relative to QemuRoot and is what the apply
// plan uses. Keeping both in one value is what lets the emit stage commit bytes and
// describe their destination without ever touching that destination.
type RenderedFile struct {
	Name   string      // artifact name, e.g. "acme_uart.c"
	Path   string      // target path relative to QemuRoot, e.g. "hw/misc/acme_uart.c"
	Kind   Kind        // KindCode for source, KindPlan for a build-file fragment
	Action ApplyAction // create for new source, modify (append) for build files
	Body   []byte
}

// Renderer is the seam the emit stage depends on. It is an interface with one pure
// method so a test can render a fixed file set, and so a future Rust template is a
// second implementation rather than a branch inside the stage.
type Renderer interface {
	Render(ir RegIR) ([]RenderedFile, error)
}

// deviceDir is where a generated device lands. It is a constant because the
// alternative — letting a request or a model pick a directory — would make the
// apply plan's path depend on untrusted input.
const deviceDir = "hw/misc"

// cRenderer emits C for a sysbus or PCI device.
type cRenderer struct{}

var _ Renderer = cRenderer{}

func NewCRenderer() Renderer { return cRenderer{} }

// Render produces the whole file set: the device source, and the two build-system
// fragments that a QEMU tree needs before it will compile it.
//
// The fragments are separate files rather than edits, because emit may not read
// the tree it is generating for. They are marked ApplyModify, which this package
// defines as "append these bytes to the target"; the applier is what verifies the
// target still has the digest the plan recorded.
func (r cRenderer) Render(ir RegIR) ([]RenderedFile, error) {
	if err := ir.Validate(); err != nil {
		return nil, err
	}
	names := newDeviceNames(ir.Device)
	source, err := renderDeviceSource(ir, names)
	if err != nil {
		return nil, err
	}
	return []RenderedFile{
		{
			Name: names.snake + ".c", Path: deviceDir + "/" + names.snake + ".c",
			Kind: KindCode, Action: ApplyCreate, Body: []byte(source),
		},
		{
			Name: "meson.build.fragment", Path: deviceDir + "/meson.build",
			Kind: KindPlan, Action: ApplyModify, Body: []byte(renderMesonFragment(names)),
		},
		{
			Name: "Kconfig.fragment", Path: deviceDir + "/Kconfig",
			Kind: KindPlan, Action: ApplyModify, Body: []byte(renderKconfigFragment(ir, names)),
		},
	}, nil
}

// deviceNames holds every spelling of the device name that C and QOM need. It is
// computed once, from the one validated field, so the type name in the .c file and
// the symbol in the meson fragment cannot drift apart.
type deviceNames struct {
	snake  string // acme_uart: file names, C symbols
	dashed string // acme-uart: the QOM type name
	upper  string // ACME_UART: macros and the QOM cast macro
	camel  string // AcmeUart: the state struct
	config string // ACME_UART: the Kconfig symbol
}

func newDeviceNames(device string) deviceNames {
	snake := strings.ToLower(device)
	upper := strings.ToUpper(snake)
	return deviceNames{
		snake:  snake,
		dashed: strings.ReplaceAll(snake, "_", "-"),
		upper:  upper,
		camel:  camelCase(snake),
		config: upper,
	}
}

// camelCase turns acme_uart into AcmeUart. It is spelled out rather than taken
// from a library because the rule has to be stable forever: the struct name is
// baked into the generated VMStateDescription, and a rename would break migration
// compatibility of anything already generated.
func camelCase(snake string) string {
	parts := strings.Split(snake, "_")
	var out strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		out.WriteString(strings.ToUpper(part[:1]))
		out.WriteString(part[1:])
	}
	return out.String()
}

// cKeywords is the small set of identifiers a register name must not become. A
// datasheet with a register called "int" or "default" is unusual but not illegal,
// and the generated struct field has to compile either way.
var cKeywords = map[string]struct{}{
	"auto": {}, "break": {}, "case": {}, "char": {}, "const": {}, "continue": {},
	"default": {}, "do": {}, "double": {}, "else": {}, "enum": {}, "extern": {},
	"float": {}, "for": {}, "goto": {}, "if": {}, "inline": {}, "int": {},
	"long": {}, "register": {}, "restrict": {}, "return": {}, "short": {},
	"signed": {}, "sizeof": {}, "static": {}, "struct": {}, "switch": {},
	"typedef": {}, "union": {}, "unsigned": {}, "void": {}, "volatile": {}, "while": {},
}

// fieldNameOf is the struct member for one register. Validate guarantees register
// names are unique case-insensitively, so lowercasing cannot collide; only a C
// keyword needs escaping, and it gets a prefix rather than a mangled spelling so
// the generated code still reads like the datasheet.
func fieldNameOf(register Register) string {
	name := strings.ToLower(register.Name)
	if _, reserved := cKeywords[name]; reserved {
		return "reg_" + name
	}
	return name
}

// cTypeOf is the storage for one register. A width is one of four values by the
// time it gets here, so the default is unreachable — it returns the widest type
// rather than panicking, because a code generator that aborts on a validated IR
// would be a worse failure than one that over-allocates.
func cTypeOf(width int) string {
	switch width {
	case 8:
		return "uint8_t"
	case 16:
		return "uint16_t"
	case 32:
		return "uint32_t"
	default:
		return "uint64_t"
	}
}

// sortedRegisters returns the registers by offset. Rendering follows address order
// rather than the order the model happened to answer in, which is what makes the
// output independent of the reply and therefore reproducible.
func sortedRegisters(ir RegIR) []Register {
	registers := append([]Register(nil), ir.Registers...)
	sort.Slice(registers, func(i, j int) bool { return registers[i].Offset < registers[j].Offset })
	return registers
}
