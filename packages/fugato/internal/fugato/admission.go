package fugato

import (
	"context"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

// admission belongs to a Manager, so copies of ControlService share the same
// budgets. Independent classes prevent long-lived logs or browser work from
// exhausting lifecycle capacity. Limits include time spent resolving projects
// or waiting for a per-runtime lock; there is no additional pending queue.
type admission struct {
	lifecycle    *operationGate
	verification *operationGate
	commands     *operationGate
	logs         *operationGate
	metadata     *operationGate
}

func newAdmission() *admission {
	return &admission{
		lifecycle:    newOperationGate(4),
		verification: newOperationGate(2),
		commands:     newOperationGate(4),
		logs:         newOperationGate(8),
		metadata:     newOperationGate(4),
	}
}

type operationGate struct{ active chan struct{} }

func newOperationGate(limit int) *operationGate {
	return &operationGate{active: make(chan struct{}, limit)}
}

// acquire never waits. The returned release must be deferred exactly once by
// the successful caller, including when an operation fails or is cancelled.
func (g *operationGate) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case g.active <- struct{}{}:
		return func() { <-g.active }, nil
	default:
		return nil, control.ErrBusy
	}
}
