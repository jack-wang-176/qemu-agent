package agent

import (
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools"
)

/* generated all component together.*/
type Agent struct {
	provider llm.Provider
	tools    *tools.Manager
	ctxmgr   *contextmgr.CompactorManager
	store    session.Store
	model    string
	maxTurns int
}

func New(provider llm.Provider, toolManager *tools.Manager, ctxmgr *contextmgr.CompactorManager, store session.Store, model string, maxTurns int) (*Agent, error) {
	if provider == nil || toolManager == nil || ctxmgr == nil || store == nil {
		return nil, fmt.Errorf("agent dependencies must not be nil")
	}
	if model == "" || maxTurns <= 0 {
		return nil, fmt.Errorf("model and maxTurns are required")
	}
	return &Agent{provider: provider, tools: toolManager, ctxmgr: ctxmgr, store: store, model: model, maxTurns: maxTurns}, nil
}
