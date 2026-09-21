package runner

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	pb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// Host is the Runner the engine host hands a StartRun's launch to: it
// decodes the wire launch through the one codec and executes it. It stands
// where the RunnerChannel client will (adapters/coordgrpc, Part 4.1, slice
// 10), which is why this file — and only this file — sees the proto.
type Host struct {
	Deps Deps
}

// Execute implements Runner.
func (h Host) Execute(ctx context.Context, wire *pb.Launch) error {
	l, err := coordgrpc.DecodeLaunch(wire)
	if err != nil {
		return fmt.Errorf("runner: %w", err)
	}
	if _, err := Execute(ctx, h.Deps, l); err != nil {
		return err
	}
	return nil
}

var _ Runner = Host{}
