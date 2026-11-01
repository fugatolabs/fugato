package control

import "errors"

// ErrBusy means an operation was rejected before starting because its class is
// at capacity. Callers may retry after allowing active operations to finish;
// the daemon never queues overload work or retries mutations automatically.
var ErrBusy = errors.New("daemon is busy; retry after active operations finish")
