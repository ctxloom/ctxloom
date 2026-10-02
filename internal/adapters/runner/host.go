package runner

import (
	"context"
	"fmt"
	"sync"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	pb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// Host is the Runner the engine host hands a StartRun's launch to: it
// decodes the wire launch through the one codec and executes it. It stands
// where the RunnerChannel client will (adapters/coordgrpc, slice
// 10), which is why this file — and only this file — sees the proto.
//
// It is also the OWNER of what the run leaves behind: Execute returns once
// the first turn is driven while the run goes on, so the Outcome is kept
// here until Teardown, at the runner process's end.
type Host struct {
	Deps Deps

	mu  sync.Mutex
	out *Outcome
}

// Execute implements Runner.
func (h *Host) Execute(ctx context.Context, wire *pb.Launch) error {
	l, err := coordgrpc.DecodeLaunch(wire)
	if err != nil {
		return fmt.Errorf("runner: %w", err)
	}
	out, err := Execute(ctx, h.Deps, l)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.out = &out
	h.mu.Unlock()
	return nil
}

// Teardown reverses what the run delivered, through the ownership record,
// and closes the endpoint it served. Main calls it once the engine host has
// closed; it runs once, and is a no-op before any launch was executed. A
// runner killed before reaching it leaves its record for the next run's
// sweep (sweepDeparted).
func (h *Host) Teardown(ctx context.Context) error {
	h.mu.Lock()
	out := h.out
	h.out = nil
	h.mu.Unlock()
	if out == nil {
		return nil
	}
	if out.Close != nil {
		defer out.Close()
	}
	if out.Delivered.Undo == nil {
		return nil
	}
	if err := out.Delivered.Undo(ctx); err != nil {
		return fmt.Errorf("runner: reverse the run's delivery: %w", err)
	}
	return nil
}

var _ Runner = (*Host)(nil)
