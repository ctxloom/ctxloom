package coordgrpc

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// runnerBuildSkew judges a runner's announced build against this
// coordinator's. ok is true only when BOTH are whole stamps
// (version.ValidStamp) and they differ: an empty or partial stamp on either
// side cannot be verified, which is not the same as a mismatch.
//
// A mismatch is REPORTED, never refused. A runner that outlived its
// coordinator re-Hellos against the re-bindable endpoint while holding live
// runs, and a refusal would strand them.
func runnerBuildSkew(coordinator, runner string) (string, bool) {
	if !version.ValidStamp(coordinator) || !version.ValidStamp(runner) || coordinator == runner {
		return "", false
	}
	msg := fmt.Sprintf("is build %s but this coordinator is build %s", runner, coordinator)
	rt, rok := version.BuildTime(runner)
	ct, cok := version.BuildTime(coordinator)
	switch {
	case rok && cok && rt.Before(ct):
		msg += " (the runner is the older build)"
	case rok && cok && ct.Before(rt):
		msg += " (the coordinator is the older build)"
	}
	return msg + "; accepted — a refusal would strand the runs it holds", true
}
