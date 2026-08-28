package runtime

// args.go — EffectRequest.Args []byte JSON → map[string]any helper.
//
// effect.go uses this helper to decode JSON arguments into the map expected by ToolRunner.

import (
	"encoding/json"
	"fmt"
)

// parseArgsToMap decodes JSON arguments into map[string]any.
// Empty input produces a non-nil empty map for ToolRunner compatibility.
func parseArgsToMap(args []byte) (map[string]any, error) {
	if len(args) == 0 {
		return map[string]any{}, nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(args, &out); err != nil {
		return nil, fmt.Errorf("parse args: %w", err)
	}
	return out, nil
}
