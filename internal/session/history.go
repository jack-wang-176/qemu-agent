package session

import (
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

/* this is to defi certain assistant boudary.*/
type Turn struct {
	/* User input, maybe many course of additonal append.*/
	Input []llm.Message
	/* Model responed contain tool need to be call.*/
	Assistant llm.Message
	Tools     []llm.Message
}

func (t *Turn) Message() []llm.Message {
	total := len(t.Input) + 1 + len(t.Tools)
	out := make([]llm.Message, 0, total)
	out = append(out, t.Input...)
	out = append(out, t.Assistant)
	out = append(out, t.Tools...)
	return out
}

/* history convert message array into managerbale and cuttable struct*/
type History struct {
	/* System promt*/
	Prefix []llm.Message
	Turns  []Turn
	/* when contextmgr invoke a certain turn maybe not crate*/
	Pending []llm.Message
}

func (h *History) History() []llm.Message {
	total := len(h.Prefix) + len(h.Pending)
	for i := 0; i < len(h.Turns); i++ {
		total += len(h.Turns[i].Input) + 1 + len(h.Turns[i].Tools)
	}
	out := make([]llm.Message, 0, total)
	out = append(out, h.Prefix...)
	for i := 0; i < len(h.Turns); i++ {
		out = append(out, h.Turns[i].Message()...)
	}
	out = append(out, h.Pending...)
	return out
}

/* ConvertIntoHistory change msg array into history*/
func ConvertIntoHistory(msgs []llm.Message) (History, error) {
	var pending []llm.Message
	var h History
	var turnseen bool
	for index := 0; index < len(msgs); index++ {
		msg := msgs[index]
		switch msg.Role {
		/* if first system definition then add to prefix or else is compact msg*/
		case llm.RoleSystem:
			{
				if turnseen && len(pending) == 0 {
					h.Prefix = append(h.Prefix, msg)
				} else {
					pending = append(pending, msg)
				}
				index++
			}
		/* use input directed.*/
		case llm.RoleUser:
			{
				pending = append(pending, msg)
				index++
			}
		/* collect both assistant and below toolinfo, the infocall should corresponding to msg record.*/
		case llm.RoleAssistant:
			{
				/* a assistant corresponding to a certain turn*/
				turnseen = true
				turn := Turn{
					Input:     pending,
					Assistant: msg,
				}
				pending = pending[:0]
				expected := make(map[string]struct{}, len(msg.ToolCalls))
				for _, tool := range msg.ToolCalls {
					if tool.Name == "" {
						return History{}, fmt.Errorf("invalid tool defination")
					}
					if _, ok := expected[tool.ID]; ok {
						return History{}, fmt.Errorf("repeated toolcallID, %s", tool.ID)
					}
					//todo 解释一下这个双括号写法
					expected[tool.ID] = struct{}{}
				}
				/* collect all the tool result below assistant msg.*/
				for index < len(msgs) && msgs[index].Role == llm.RoleTool {
					toolMsg := msgs[index]
					if toolMsg.ToolCallID == "" {
						return History{}, fmt.Errorf("message %d: tool message has empty tool_call_id", index)
					}
					if _, match := expected[toolMsg.ToolCallID]; !match {
						return History{}, fmt.Errorf("miss match of toolCall ID")
					}
					delete(expected, toolMsg.ToolCallID)
					index++
				}
				if len(expected) > 0 {
					return History{}, fmt.Errorf("mismatch of tool and toolcall")
				}
				h.Turns = append(h.Turns, turn)
			}
		case llm.RoleTool:
			{
				return History{}, fmt.Errorf("do not expected to reach roletool in switch")
			}
		}
	}
	h.Pending = append(h.Pending, pending...)
	return h, nil
}
