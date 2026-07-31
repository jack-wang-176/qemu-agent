package modeling

// renderer_test.go pins the template rules that a reviewer cannot check by eye. The
// generated file is long, so the tests assert on the specific lines that encode a
// decision — an access rule, an undocumented reset value, a bus-dependent parent —
// rather than on the whole output.

import (
	"strings"
	"testing"
)

// renderOne renders the baseline IR and returns the device source.
func renderOne(t *testing.T, ir RegIR) string {
	t.Helper()
	files, err := NewCRenderer().Render(ir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name, ".c") {
			return string(file.Body)
		}
	}
	t.Fatal("no C source in the rendered file set")
	return ""
}

// TestRenderIsDeterministic is invariant 8: the renderer is a pure function, so the
// same IR must produce the same bytes — that is what makes the content address of a
// re-run stable and `/modeling diff` meaningful.
func TestRenderIsDeterministic(t *testing.T) {
	ir := validIR()
	first, err := NewCRenderer().Render(ir)
	if err != nil {
		t.Fatal(err)
	}
	// Reorder the registers: address order is what the renderer follows, so the
	// order the model happened to answer in must not change a byte.
	shuffled := validIR()
	shuffled.Registers[0], shuffled.Registers[2] = shuffled.Registers[2], shuffled.Registers[0]
	second, err := NewCRenderer().Render(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("file counts differ: %d vs %d", len(first), len(second))
	}
	for index := range first {
		if first[index].Name != second[index].Name {
			t.Fatalf("file %d: name %q vs %q", index, first[index].Name, second[index].Name)
		}
		if string(first[index].Body) != string(second[index].Body) {
			t.Fatalf("file %s is not reproducible", first[index].Name)
		}
	}
}

func TestRenderRejectsInvalidIR(t *testing.T) {
	ir := validIR()
	ir.Registers[1].Offset = 5 // misaligned
	if _, err := NewCRenderer().Render(ir); Category(err) != "schema_invalid" {
		t.Fatalf("the renderer must re-validate its input, got %v", err)
	}
}

// TestRenderedAccessorsFollowTheAccessEnum is the rule that decides what the device
// actually does. Each access kind gets one assertion, because a template that got
// w1c wrong would produce a device that looks right and hangs a driver.
func TestRenderedAccessorsFollowTheAccessEnum(t *testing.T) {
	ir := validIR()
	ir.Registers = append(ir.Registers, Register{
		Name: "INTCLR", Offset: 12, Width: 32, Access: AccessW1C,
	})
	source := renderOne(t, ir)

	// rw: readable and writable through the state field.
	if !strings.Contains(source, "        return s->ctrl;") {
		t.Fatal("a read/write register must be readable")
	}
	if !strings.Contains(source, "        s->ctrl = (uint32_t)value;") {
		t.Fatal("a read/write register must be writable")
	}
	// ro: a write logs and changes nothing.
	if !strings.Contains(source, `write to ro register STATUS`) {
		t.Fatalf("a write to a read-only register must log:\n%s", source)
	}
	if strings.Contains(source, "s->status = (uint32_t)value;") {
		t.Fatal("a read-only register must not be assigned by a guest write")
	}
	// wo: a read returns 0 and logs.
	if !strings.Contains(source, `read of wo-only register DATA`) {
		t.Fatal("a read of a write-only register must log")
	}
	// w1c: a write clears the bits that were set.
	if !strings.Contains(source, "s->intclr &= ~(uint32_t)value;") {
		t.Fatalf("write-1-to-clear must clear, not assign:\n%s", source)
	}
}

// TestRenderedResetKeepsUnknownValuesUnknown is invariant 5 reaching the C file: a
// register with no documented reset value is annotated, never given an invented one.
func TestRenderedResetKeepsUnknownValuesUnknown(t *testing.T) {
	ir := validIR()
	ir.Registers[0].Reset = nil
	source := renderOne(t, ir)
	if !strings.Contains(source, "reset value of CTRL is undocumented") {
		t.Fatalf("an unknown reset value must be marked in the code:\n%s", source)
	}
}

// TestRenderedEffectIsATODONotCode: a documented side effect becomes a comment. The
// generator refuses to invent the behaviour, because plausible-looking generated
// behaviour is the one thing a reviewer cannot catch.
func TestRenderedEffectIsATODONotCode(t *testing.T) {
	ir := validIR()
	ir.Registers[0].Effect = "starts the transmitter\nand raises irq */ evil"
	source := renderOne(t, ir)
	if !strings.Contains(source, "/* TODO: documented side effect — starts the transmitter and raises irq * / evil */") {
		t.Fatalf("the effect must be a flattened, comment-safe TODO:\n%s", source)
	}
	// The comment terminator inside the datasheet text must not end the comment.
	if strings.Contains(source, "irq */ evil") {
		t.Fatal("a comment terminator from the datasheet leaked into the source")
	}
}

// TestRenderedSysbusAndPCIDiffer covers the bus-dependent half: the two templates
// must not borrow each other's constructor calls.
func TestRenderedSysbusAndPCIDiffer(t *testing.T) {
	sysbus := renderOne(t, validIR())
	if !strings.Contains(sysbus, "sysbus_init_mmio(sbd, &s->iomem);") {
		t.Fatal("a sysbus device must publish its MMIO region")
	}
	if strings.Contains(sysbus, "pci_register_bar") {
		t.Fatal("a sysbus device must not register a PCI BAR")
	}

	pciIR := validIR()
	pciIR.BusKind = BusPCI
	pci := renderOne(t, pciIR)
	if !strings.Contains(pci, "pci_register_bar(pci_dev, 0, PCI_BASE_ADDRESS_SPACE_MEMORY, &s->iomem);") {
		t.Fatalf("a PCI device must register a BAR:\n%s", pci)
	}
	if strings.Contains(pci, "sysbus_init_mmio") {
		t.Fatal("a PCI device must not call sysbus_init_mmio")
	}
	if !strings.Contains(pci, "INTERFACE_CONVENTIONAL_PCI_DEVICE") {
		t.Fatal("a PCI device must declare the conventional PCI interface")
	}
}

// TestRenderedNamesAreConsistent: every spelling of the device name is derived from
// the one validated field, so the type name, struct name and file name agree.
func TestRenderedNamesAreConsistent(t *testing.T) {
	files, err := NewCRenderer().Render(validIR())
	if err != nil {
		t.Fatal(err)
	}
	source := ""
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
		if strings.HasSuffix(file.Name, ".c") {
			source = string(file.Body)
			if file.Name != "acme_uart.c" || file.Path != "hw/misc/acme_uart.c" {
				t.Fatalf("unexpected device file: %s at %s", file.Name, file.Path)
			}
			if file.Action != ApplyCreate || file.Kind != KindCode {
				t.Fatalf("the device source must be a created code artifact: %+v", file)
			}
		}
	}
	for _, want := range []string{
		`#define TYPE_ACME_UART "acme-uart"`,
		"OBJECT_DECLARE_SIMPLE_TYPE(AcmeUartState, ACME_UART)",
		"struct AcmeUartState {",
		"static const VMStateDescription vmstate_acme_uart = {",
		"type_init(acme_uart_register_types)",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("missing %q in:\n%s", want, source)
		}
	}
	// The build-file fragments are appends to existing files, and their paths are
	// the ones the applier will check against the tree.
	if strings.Join(paths, ",") != "hw/misc/acme_uart.c,hw/misc/meson.build,hw/misc/Kconfig" {
		t.Fatalf("unexpected target paths: %v", paths)
	}
}

// TestRenderedFieldNameEscapesCKeywords: a datasheet may name a register "int".
func TestRenderedFieldNameEscapesCKeywords(t *testing.T) {
	ir := validIR()
	ir.Registers[1].Name = "int"
	source := renderOne(t, ir)
	if !strings.Contains(source, "uint32_t reg_int;") {
		t.Fatalf("a C keyword register name must be escaped:\n%s", source)
	}
}

// TestRenderedNotesReachTheSource: the datasheet's gaps travel with the code, so the
// person who completes the device by hand does not have to find the project first.
func TestRenderedNotesReachTheSource(t *testing.T) {
	ir := validIR()
	ir.Notes = []string{"offsets 0x40..0x4c are undocumented"}
	source := renderOne(t, ir)
	if !strings.Contains(source, " *  - offsets 0x40..0x4c are undocumented") {
		t.Fatalf("notes must be carried into the source:\n%s", source)
	}
}

func TestRenderedFragmentsAreAppends(t *testing.T) {
	files, err := NewCRenderer().Render(validIR())
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name == "meson.build.fragment" {
			if file.Action != ApplyModify {
				t.Fatal("a build-file fragment must be an append, not a create")
			}
			if !strings.Contains(string(file.Body), "files('acme_uart.c')") {
				t.Fatalf("the meson fragment must name the generated file: %s", file.Body)
			}
		}
		if file.Name == "Kconfig.fragment" && !strings.Contains(string(file.Body), "config ACME_UART") {
			t.Fatalf("the Kconfig fragment must declare the symbol: %s", file.Body)
		}
	}
}
