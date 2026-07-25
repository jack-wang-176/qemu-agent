package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type BashAnalyzer interface {
	Analyze(rawArgs string) (Assessment, error)
}

type ConservativeBashAnalyzer struct {
	mode string
}

func NewConservativeBashAnalyzer(mode string) (*ConservativeBashAnalyzer, error) {
	switch mode {
	case "allow", "deny-dangerous", "ask-dangerous":
	default:
		return nil, fmt.Errorf("unsupported security mode %q", mode)
	}
	return &ConservativeBashAnalyzer{mode: mode}, nil
}

func decodeBashCommand(raw string) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return "", fmt.Errorf("decode bash arguments: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("bash arguments contain extra JSON values")
		}
		return "", fmt.Errorf("decode trailing bash arguments: %w", err)
	}
	args.Command = strings.TrimSpace(args.Command)
	if args.Command == "" {
		return "", errors.New("bash command is empty")
	}
	return args.Command, nil
}

func (a *ConservativeBashAnalyzer) Analyze(raw string) (Assessment, error) {
	command, err := decodeBashCommand(raw)
	if err != nil {
		return Assessment{}, err
	}
	lower := strings.ToLower(command)
	summary := truncate(command, 300)

	for _, pattern := range []string{
		"rm -rf /", "rm -rf /*", "rm -rf ~", "mkfs", "fdisk", "parted",
		"shutdown", "reboot", "poweroff", "sudo ", "doas ",
		"| sh", "| bash", "| zsh", "> /etc/", ">/etc/",
	} {
		if strings.Contains(lower, pattern) {
			return Assessment{Decision: DecisionDeny, Rule: "bash-deny-pattern", Reason: "command matches a prohibited destructive pattern", Summary: summary}, nil
		}
	}

	complex := strings.ContainsAny(command, "\n;`") ||
		strings.Contains(command, "&&") || strings.Contains(command, "||") ||
		strings.Contains(command, "$(") || strings.Contains(command, "<<")
	if !complex {
		for _, prefix := range []string{
			"pwd", "ls", "cat", "head", "tail", "wc", "grep", "rg", "find",
			"git status", "git diff", "git log", "git show",
			"go test", "go vet", "go build", "gofmt -d",
		} {
			if lower == prefix || strings.HasPrefix(lower, prefix+" ") {
				return Assessment{Decision: DecisionAllow, Rule: "bash-readonly", Reason: "command is in the conservative read-only allowlist", Summary: summary}, nil
			}
		}
	}

	if a.mode == "allow" {
		return Assessment{Decision: DecisionAllow, Rule: "bash-mode-allow", Reason: "security mode allows unclassified bash commands", Summary: summary}, nil
	}
	return Assessment{Decision: DecisionAsk, Rule: "bash-unclassified", Reason: "command cannot be proven safe", Summary: summary}, nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...[truncated]"
}
