package runtime

// args.go — EffectRequest.Args []byte JSON → map[string]any helper.
//
// effect.go 依赖此 helper 把 JSON args 解析为 ToolRunner 期望的 map。

import (
	"encoding/json"
	"fmt"
)

// parseArgsToMap 把 []byte JSON 解析为 map[string]any。
// 空输入返回空 map（不是 nil），便于 ToolRunner 接受。
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
