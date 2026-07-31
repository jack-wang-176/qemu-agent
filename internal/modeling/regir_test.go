package modeling

// regir_test.go pins the rules of the register IR. Validate is the only gate
// between a model reply and a device that will be compiled, so each rule gets a
// case that must fail and the valid baseline is asserted separately — a validator
// that rejects everything would pass a table of negatives alone.

import (
	"strings"
	"testing"
)

// validIR is the baseline every negative case mutates. It is a function rather
// than a package variable so a test cannot corrupt the fixture for the next one.
func validIR() RegIR {
	reset := uint64(0)
	return RegIR{
		Device: "acme_uart", BusKind: BusSysbus, MMIOSize: 4096,
		Registers: []Register{
			{Name: "CTRL", Offset: 0, Width: 32, Access: AccessRW, Reset: &reset,
				Fields: []Field{{Name: "ENABLE", Bit: 0, Width: 1, Description: "starts the device"}},
				Effect: "writing ENABLE starts the transmitter"},
			{Name: "STATUS", Offset: 4, Width: 32, Access: AccessRO},
			{Name: "DATA", Offset: 8, Width: 8, Access: AccessWO},
		},
		Interrupts: []IRQ{{Name: "irq", Index: 0, Description: "raised on receive"}},
	}
}

func TestRegIRValidateAcceptsBaseline(t *testing.T) {
	if err := validIR().Validate(); err != nil {
		t.Fatalf("baseline IR must validate: %v", err)
	}
}

func TestRegIRValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RegIR)
	}{
		{"Overlap", func(ir *RegIR) {
			// A 64-bit register at 0 covers 0..7, which swallows STATUS at 4.
			ir.Registers[0].Width = 64
		}},
		{"Misalignment", func(ir *RegIR) {
			ir.Registers[1].Offset = 5
		}},
		{"BadWidth", func(ir *RegIR) {
			ir.Registers[1].Width = 24
		}},
		{"DuplicateName", func(ir *RegIR) {
			// Case-insensitive: a C identifier collision is a build failure later.
			ir.Registers[1].Name = "ctrl"
		}},
		{"OutOfRange", func(ir *RegIR) {
			ir.Registers[1].Offset = 4096
		}},
		{"UnknownAccess", func(ir *RegIR) {
			ir.Registers[1].Access = Access("secret")
		}},
		{"BadDevice", func(ir *RegIR) {
			ir.Device = "Acme UART"
		}},
		{"BadBus", func(ir *RegIR) {
			ir.BusKind = BusKind("i2c")
		}},
		{"MMIOSizeNotPowerOfTwo", func(ir *RegIR) {
			ir.MMIOSize = 4095
		}},
		{"NoRegisters", func(ir *RegIR) {
			ir.Registers = nil
		}},
		{"ResetTooWide", func(ir *RegIR) {
			wide := uint64(0x1ff)
			ir.Registers[2].Reset = &wide // DATA is 8 bits
		}},
		{"FieldOutsideWidth", func(ir *RegIR) {
			ir.Registers[0].Fields[0].Bit = 32
		}},
		{"FieldOverlap", func(ir *RegIR) {
			ir.Registers[0].Fields = append(ir.Registers[0].Fields,
				Field{Name: "MODE", Bit: 0, Width: 2})
		}},
		{"DuplicateIRQIndex", func(ir *RegIR) {
			ir.Interrupts = append(ir.Interrupts, IRQ{Name: "irq2", Index: 0})
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ir := validIR()
			testCase.mutate(&ir)
			err := ir.Validate()
			if err == nil {
				t.Fatal("expected the IR to be rejected")
			}
			// Every structural rejection has to classify as schema_invalid, because
			// that is the category the pipeline stores and the user sees.
			if got := Category(err); got != "schema_invalid" {
				t.Fatalf("category = %q, want schema_invalid", got)
			}
		})
	}
}

// TestRegIRNormalizeClampsInsteadOfFailing covers rule 6: bounded prose problems
// are repaired and recorded, never turned into a stage failure.
func TestRegIRNormalizeClampsInsteadOfFailing(t *testing.T) {
	ir := validIR()
	ir.Device = "  ACME_UART "
	ir.BusKind = BusKind("SYSBUS")
	ir.Registers[0].Effect = strings.Repeat("x", maxEffectBytes+100)
	added := ir.Normalize()
	if ir.Device != "acme_uart" || ir.BusKind != BusSysbus {
		t.Fatalf("identity not normalized: %q %q", ir.Device, ir.BusKind)
	}
	if len(added) == 0 {
		t.Fatal("truncating an effect must add a note")
	}
	if err := ir.Validate(); err != nil {
		t.Fatalf("normalized IR must validate: %v", err)
	}
}

// TestOpenQuestionsReportsMissingResets is the derived-not-stored property: the
// questions follow the IR, so filling a reset value removes its question without
// anybody editing a list.
func TestOpenQuestionsReportsMissingResets(t *testing.T) {
	ir := validIR()
	ir.Registers[1].Reset = nil // STATUS, read-only but still documented
	questions := strings.Join(ir.OpenQuestions(), "\n")
	if !strings.Contains(questions, "STATUS") {
		t.Fatalf("missing reset value not reported: %s", questions)
	}
	// DATA is write-only, so its missing reset value is not a question.
	if strings.Contains(questions, "register DATA has no documented reset") {
		t.Fatalf("write-only register should not need a reset value: %s", questions)
	}
	reset := uint64(1)
	ir.Registers[1].Reset = &reset
	if strings.Contains(strings.Join(ir.OpenQuestions(), "\n"), "STATUS has no documented reset") {
		t.Fatal("a filled reset value must stop being an open question")
	}
}

func TestClampTextKeepsRunesIntact(t *testing.T) {
	// A byte limit that lands inside a multi-byte rune must cut before it, not
	// produce a replacement glyph in a Telegram message.
	clamped, cut := clampText("寄存器映射", 7)
	if !cut {
		t.Fatal("expected the text to be clamped")
	}
	if !strings.HasSuffix(clamped, "…") {
		t.Fatalf("clamped text must be marked: %q", clamped)
	}
	for _, r := range clamped {
		if r == '�' {
			t.Fatalf("clamp broke a rune: %q", clamped)
		}
	}
}
