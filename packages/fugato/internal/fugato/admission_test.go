package fugato

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

func TestOperationGateRejectsOverloadAndRecovers(t *testing.T) {
	g := newOperationGate(4)
	var wg sync.WaitGroup
	releases := make(chan func(), 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := g.acquire(context.Background())
			if err == nil {
				releases <- release
			} else if !errors.Is(err, control.ErrBusy) {
				t.Errorf("unexpected admission error: %v", err)
			}
		}()
	}
	wg.Wait()
	close(releases)
	if got := len(releases); got != 4 {
		t.Fatalf("admitted %d concurrent operations, want 4", got)
	}
	for release := range releases {
		release()
	}
	release, err := g.acquire(context.Background())
	if err != nil {
		t.Fatalf("capacity was not released: %v", err)
	}
	defer release()
}

func TestOperationGateRejectsCancelledContextWithoutTakingCapacity(t *testing.T) {
	g := newOperationGate(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	release, err := g.acquire(context.Background())
	if err != nil {
		t.Fatalf("cancelled call occupied capacity: %v", err)
	}
	defer release()
}
