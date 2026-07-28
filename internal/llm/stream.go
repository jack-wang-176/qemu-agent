package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	MaxStreamTextBytes       = 16 * 1024 * 1024
	MaxToolCallsPerResponse  = 128
	MaxToolCallArgumentBytes = 4 * 1024 * 1024
)

type StreamAccumulator struct {
	text      strings.Builder
	toolCalls map[int]*toolCallAccumulator
	usage     *Usage
	done      bool
	finalized bool
}

type toolCallAccumulator struct {
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
}

func NewStreamAccumulator() *StreamAccumulator {
	return &StreamAccumulator{toolCalls: make(map[int]*toolCallAccumulator)}
}

func (a *StreamAccumulator) Apply(event StreamEvent) error {
	if a == nil {
		return errors.New("stream accumulator is nil")
	}
	if a.done {
		return errors.New("stream event received after done")
	}
	if a.toolCalls == nil {
		a.toolCalls = make(map[int]*toolCallAccumulator)
	}
	if a.text.Len()+len(event.TextDelta) > MaxStreamTextBytes {
		return fmt.Errorf("stream text exceeds %d bytes", MaxStreamTextBytes)
	}
	a.text.WriteString(event.TextDelta)
	for _, delta := range event.ToolCallDeltas {
		if delta.Index < 0 || delta.Index >= MaxToolCallsPerResponse {
			return fmt.Errorf("tool call index %d is out of range", delta.Index)
		}
		call := a.toolCalls[delta.Index]
		if call == nil {
			call = &toolCallAccumulator{}
			a.toolCalls[delta.Index] = call
		}
		if call.arguments.Len()+len(delta.Arguments) > MaxToolCallArgumentBytes {
			return fmt.Errorf("tool call %d arguments exceed %d bytes", delta.Index, MaxToolCallArgumentBytes)
		}
		call.id.WriteString(delta.ID)
		call.name.WriteString(delta.Name)
		call.arguments.WriteString(delta.Arguments)
	}
	if event.Usage != nil {
		if event.Usage.PromptToken < 0 || event.Usage.CompletionToken < 0 || event.Usage.TotalToken < 0 {
			return errors.New("stream usage contains negative token count")
		}
		if a.usage != nil && *a.usage != *event.Usage {
			return errors.New("stream usage changed after it was reported")
		}
		usage := *event.Usage
		a.usage = &usage
	}
	if event.Done {
		a.done = true
	}
	return nil
}

func (a *StreamAccumulator) Done() bool { return a != nil && a.done }

func (a *StreamAccumulator) Finalize() (*Response, error) {
	if a == nil {
		return nil, errors.New("stream accumulator is nil")
	}
	if !a.done {
		return nil, errors.New("stream is not done")
	}
	if a.finalized {
		return nil, errors.New("stream accumulator is already finalized")
	}

	indexes := make([]int, 0, len(a.toolCalls))
	for index := range a.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	orderedCalls := make([]ToolCall, 0, len(indexes))
	seenIDs := make(map[string]struct{}, len(indexes))
	for expected, index := range indexes {
		if index != expected {
			return nil, fmt.Errorf("tool call index gap: expected %d, got %d", expected, index)
		}
		call := a.toolCalls[index]
		id, name, arguments := call.id.String(), call.name.String(), call.arguments.String()
		if id == "" || name == "" {
			return nil, fmt.Errorf("tool call %d is incomplete", index)
		}
		if _, exists := seenIDs[id]; exists {
			return nil, fmt.Errorf("duplicate tool call id %q", id)
		}
		seenIDs[id] = struct{}{}
		if arguments == "" || !json.Valid([]byte(arguments)) {
			return nil, fmt.Errorf("tool call %d arguments are not valid JSON", index)
		}
		orderedCalls = append(orderedCalls, ToolCall{ID: id, Name: name, Args: arguments})
	}
	usage := Usage{}
	if a.usage != nil {
		usage = *a.usage
	}
	a.finalized = true
	return &Response{
		Message: Message{
			Role:      RoleAssistant,
			Content:   a.text.String(),
			ToolCalls: orderedCalls,
		},
		Usage: usage,
	}, nil
}
