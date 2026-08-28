package modelingapp

import (
	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

func deriveContext(call modelingapi.CallContext, method string) (pipelineapi.Scope, pipelineapi.InvocationContext, error) {
	err := call.Validate(modelingapi.MutationKindOf(method))
	if err != nil {
		return pipelineapi.Scope{}, pipelineapi.InvocationContext{}, err
	}
	return pipelineapi.Scope{
			WorkspaceID: call.WorkspaceID,
			UserID:      call.UserID,
		}, pipelineapi.InvocationContext{
			RequestID:   call.RequestID,
			TraceID:     call.TraceID,
			SessionID:   call.SessionID,
			SessionKey:  call.SessionKey,
			Channel:     call.Channel,
			Interactive: call.Interactive,
		}, nil
}
