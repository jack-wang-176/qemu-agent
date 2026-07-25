package security

import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/tools"
)

type Policy interface {
	Evaluate(context.Context, Invocation, tools.Tool) (Assessment, error)
}

type StaticPolicy struct {
	mode string
	bash BashAnalyzer
}

func NewStaticPolicy(mode string, bash BashAnalyzer) (*StaticPolicy, error) {
	switch mode {
	case "allow", "deny-dangerous", "ask-dangerous":
	default:
		return nil, fmt.Errorf("unsupported security mode %q", mode)
	}
	if bash == nil {
		return nil, errors.New("bash analyzer is nil")
	}
	return &StaticPolicy{mode: mode, bash: bash}, nil
}

// Evaluate makes a side-effect-free decision before a tool is invoked.
func (p *StaticPolicy) Evaluate(ctx context.Context, in Invocation, tool tools.Tool) (Assessment, error) {
	if err := ctx.Err(); err != nil {
		return Assessment{}, err
	}
	if tool == nil {
		return Assessment{}, errors.New("policy tool is nil")
	}
	if tool.Name() != in.ToolName {
		return Assessment{}, fmt.Errorf(
			"policy tool mismatch: invocation=%q tool=%q",
			in.ToolName, tool.Name(),
		)
	}
	// if bash then use interface to assess bash command securty
	if tool.Name() == "bash" {
		return p.bash.Analyze(in.Arguments)
	}
	if !tool.Dangerous() {
		return Assessment{
			Decision: DecisionAllow,
			Rule:     "safe-tool",
			Reason:   "tool is marked non-dangerous",
		}, nil
	}
	switch p.mode {
	case "allow":
		return Assessment{Decision: DecisionAllow, Rule: "mode-allow", Reason: "security mode allows dangerous tools"}, nil
	case "deny-dangerous":
		return Assessment{Decision: DecisionDeny, Rule: "dangerous-tool", Reason: "dangerous tools are disabled"}, nil
	case "ask-dangerous":
		return Assessment{Decision: DecisionAsk, Rule: "dangerous-tool", Reason: "dangerous tool requires approval"}, nil
	default:
		return Assessment{}, fmt.Errorf("unsupported security mode %q", p.mode)
	}
}
