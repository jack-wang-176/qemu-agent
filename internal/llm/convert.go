package llm

import (
	"github.com/openai/openai-go/v3"
)

/* toOpenAIParaks change the req into openai defination,
 * the outside use string and inside use openai struct to
 * invoke.
 */
func toOpenAIParams(req Request) openai.ChatCompletionNewParams {
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages))
	for _, msg := range req.Messages {
		/* the role data corresponding to MessageParamUnion of__ field*/
		switch msg.Role {
		case RoleUser:
			{
				msgs = append(msgs, openai.UserMessage(msg.Content))
			}
		case RoleSystem:
			{
				msgs = append(msgs, openai.SystemMessage(msg.Content))
			}
		case RoleTool:
			{
				msgs = append(msgs, openai.ToolMessage(msg.Content, msg.ToolCallID))
			}
		case RoleAssistant:
			{
				am := openai.ChatCompletionAssistantMessageParam{}
				if msg.Content != "" {
					am.Content.OfString = openai.String(msg.Content)
				}
				for _, tc := range msg.ToolCalls {
					am.ToolCalls = append(am.ToolCalls,
						openai.ChatCompletionMessageToolCallUnionParam{
							OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
								ID: tc.ID,
								Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
									Arguments: tc.Args,
									Name:      tc.Name,
								},
							},
						})
				}
				msgs = append(msgs, openai.ChatCompletionMessageParamUnion{OfAssistant: &am})
			}
		}
	}
	p := openai.ChatCompletionNewParams{
		Model: req.Model, Messages: msgs,
	}

	if len(req.Tools) > 0 {
		p.Tools = toOpenAITools(req.Tools)
	}

	if req.MaxTokens > 0 {
		p.MaxCompletionTokens = openai.Int(int64(req.MaxTokens))
	}
	return p
}

/* change the defined toolschema into openai struct,
 * complex json struct was done in tools/manager.go
 */
func toOpenAITool(schemas ToolSchema) openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
		Name:        schemas.Name,
		Description: openai.String(schemas.Description),
		Parameters:  openai.FunctionParameters(schemas.Parameters),
	})
}

func toOpenAITools(schemas []ToolSchema) []openai.ChatCompletionToolUnionParam {
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(schemas))
	for _, s := range schemas {
		out = append(out, toOpenAITool(s))
	}
	return out
}

/* fromOpenAIResponse change the resp into response openai strcut
 * with toOpenAIParam implent the interaction between openai system.
 */
func fromOpenAIResponse(resp *openai.ChatCompletion) *Response {
	return &Response{
		Usage: Usage{
			PromptToken:     resp.Usage.PromptTokens,
			CompletionToken: resp.Usage.CompletionTokens,
			TotalToken:      resp.Usage.TotalTokens,
		},
		Message: fromOpenAIMessage(resp.Choices[0].Message),
	}
}
func fromOpenAIMessage(m openai.ChatCompletionMessage) Message {
	var msg Message
	var toolCall ToolCall
	msg.Role = Role(m.Role)
	msg.Content = m.Content
	for _, respToolcall := range m.ToolCalls {
		toolCall.ID = respToolcall.ID
		toolCall.Name = respToolcall.Function.Name
		toolCall.Args = respToolcall.Function.Arguments
		msg.ToolCalls = append(msg.ToolCalls, toolCall)
	}
	return msg
}
