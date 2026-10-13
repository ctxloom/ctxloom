package runner

import (
	"context"
	"fmt"
	"sync"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	pb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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

// Execute implements Runner. Everything executing the launch reports still
// reaches the runner's own sink (Deps.Reporter); the rendered ones are also
// returned, for the StartRun answer to relay. A Quiet finding is not: it is
// recorded, never shown, wherever it is reported.
func (h *Host) Execute(ctx context.Context, wire *pb.Launch) (report.Findings, error) {
	l, err := coordgrpc.DecodeLaunch(wire)
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}
	var relay report.Collector
	deps := h.Deps
	own := report.To(h.Deps.Reporter)
	deps.Reporter = report.SinkFunc(func(f report.Finding) {
		own.Report(f)
		if !f.Quiet {
			relay.Report(f)
		}
	})
	out, err := Execute(ctx, deps, l)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.out = &out
	h.mu.Unlock()
	return relay.Drain(), nil
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
