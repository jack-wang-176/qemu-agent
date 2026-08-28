package modelingapp

import (
	"context"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	//"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

type ReceiptStore interface {
	Load(context.Context, string) (modelingapi.OperationResult, bool, error)
	Save(context.Context, string, modelingapi.OperationResult) error
}

func (s *Service) loadReceipt(ctx context.Context, call modelingapi.CallContext, method string) (modelingapi.OperationResult, bool, error) {
	// TODO: derive a canonical key bound to method, scope, project and request.
	panic("TODO")
}

func (s *Service) saveReceipt(ctx context.Context, key string, result modelingapi.OperationResult) error {
	// TODO: clone the public result before persistence; never save internal values.
	panic("TODO")
}
