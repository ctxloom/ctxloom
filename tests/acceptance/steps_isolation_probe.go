//go:build acceptance

// Godog wiring for features/probes/isolation_probe.feature. All decision logic
// (auth-path resolution, the live run, the four-guarantee assertions) lives
// in isolation_probe.go, untangled from godog — this file only translates
// Gherkin steps into calls against it, and prints the one loud per-cell
// report line every scenario ends on, pass, fail, or skip.
package acceptance

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// probeCellState is this feature's per-scenario fixture state.
type probeCellState struct {
	Engine     string
	Axis       probeAxis
	ForcedPath probeAuthPath
	Result     *probeResult
}

func probeStateOf(w *World) *probeCellState {
	if w.probe == nil {
		w.probe = &probeCellState{}
	}
	return w.probe
}

// probeSkip prints the loud, specific skip line (engine, axis, authPath,
// reason) and returns godog.ErrSkip — so a run where every cell skips is
// grep-distinct from a run where every cell passed, per this feature's own
// doc. authPath is the SKIP's real resolved auth path (this used to
// be hardcoded to probeAuthNone regardless of caller, so the forced-env-key
// scenario's skip -- which fires precisely because a credential IS present
// but not the one this scenario forces -- misreported authPath=no-credentials
// instead of the real seeded/env-key path, misattributing why the cell was
// skipped).
func probeSkip(engine string, axis probeAxis, authPath probeAuthPath, reason string) error {
	printProbeReport(engine, axis, authPath, reason, "SKIPPED", "")
	return godog.ErrSkip
}

func registerIsolationProbeSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the isolation probe targets "([^"]*)" under the "([^"]*)" axis$`, func(c context.Context, engine, axisStr string) error {
		w := worldFrom(c)
		p := probeStateOf(w)
		p.Engine, p.Axis = engine, probeAxis(axisStr)

		authPath, reason, err := probeTargetAuth(engine, p.Axis)
		if err != nil {
			return err
		}
		if authPath == probeAuthNone {
			return probeSkip(engine, p.Axis, authPath, reason)
		}
		return nil
	})

	ctx.Step(`^the isolation probe targets "([^"]*)" under the "worktree" axis using its API key credential$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		p := probeStateOf(w)
		p.Engine, p.Axis, p.ForcedPath = engine, probeAxisWorktree, probeAuthEnvKey

		authPath, reason, err := probeTargetAuth(engine, p.Axis)
		if err != nil {
			return err
		}
		if authPath != probeAuthEnvKey {
			return probeSkip(engine, p.Axis, authPath, fmt.Sprintf("this scenario forces the env-API-key bypass path specifically, but it is not the ambient path (%s) — %s", authPath, reason))
		}
		return nil
	})

	ctx.Step(`^the probe runs it live, writing a unique token in one turn$`, func(c context.Context) error {
		w := worldFrom(c)
		p := probeStateOf(w)
		var res *probeResult
		var err error
		switch {
		case p.Axis == probeAxisWorktree:
			res, err = runProbeWorktree(w, p.Engine, p.ForcedPath)
		case isProbeContainerAxis(p.Axis):
			// Resolve the SAME way probeCellGate does for the matrix probes
			// (capability_probe_gate_live.go's probeContainerRuntimeForAxis):
			// no second copy of the ownership-vs-reachability policy, and no
			// substituting the other ownership mode for the one this cell
			// asked for. This used to hardcode "docker" with no
			// reachability OR ownership check at all, so a podman-only host
			// (or a rootful-only host answering for a rootless cell) failed
			// the cell as a probe FAILURE rather than skipping with a named
			// reason — the opposite of this file's own loudness contract.
			rt, decision, msg := probeContainerRuntimeForAxis(c, string(p.Axis), fmt.Sprintf("the isolation probe's %s/%s cell", p.Engine, p.Axis))
			switch decision {
			case dockergate.Fail:
				return fmt.Errorf("isolation probe: %s", msg)
			case dockergate.Skip:
				return probeSkip(p.Engine, p.Axis, probeAuthNone, msg)
			}
			res, err = runProbeContainer(w, p.Engine, p.Axis, rt.Command)
		default:
			return fmt.Errorf("isolation probe: unknown axis %q", p.Axis)
		}
		if err != nil {
			return fmt.Errorf("isolation probe: %w", err)
		}
		p.Result = res
		return nil
	})

	ctx.Step(`^the probe's core guarantees hold for "([^"]*)" under the "([^"]*)" axis$`, func(c context.Context, engine, axisStr string) error {
		w := worldFrom(c)
		p := probeStateOf(w)
		res := p.Result
		if res == nil {
			return fmt.Errorf("isolation probe: no result recorded — the When step never ran")
		}

		var assertErr error
		switch p.Axis {
		case probeAxisWorktree:
			assertErr = assertProbeWorktree(res)
		case probeAxisContainerRootless, probeAxisContainerRootful:
			assertErr = assertProbeContainer(res)
			// The enumerated `docker diff` write-set, paths only — printed
			// unconditionally (pass or fail) so a real run leaves behind
			// the concrete evidence assertion (c)/(d) is built on, not just
			// a verdict. This is the one place this probe's own output
			// carries container-internal paths; still never file content.
			if res.Container.Name != "" {
				fmt.Printf("  docker diff %s (%d entries):\n", res.Container.Name, len(res.Container.Diff))
				for _, line := range res.Container.Diff {
					fmt.Printf("    %s\n", line)
				}
			}
			// The strace-observed READ SET — which context surfaces the real CLI
			// actually OPENED/stat'd/probed, INCLUDING the ENOENT rows for paths
			// it looked for and did not find. This is the half docker diff can
			// never show; printed unconditionally so a real run leaves the
			// concrete read evidence behind (paths only, never file content).
			fmt.Printf("  strace read-set (%d unique reads%s):\n", len(res.Reads),
				map[bool]string{true: ", incl. ENOENT/EACCES probes", false: ""}[probeReadsHasFailedResult(res.Reads)])
			for _, r := range res.Reads {
				fmt.Printf("    %-11s %-6s %s\n", r.Syscall, r.Result, r.Path)
			}
			if res.ReadsErr != "" {
				fmt.Printf("    (read-observation note: %s)\n", res.ReadsErr)
			}
			// (b) the token file, for the container axis, is a plain bind
			// mount at the project dir's own identical path — checked
			// directly against the TestEnvironment, no race involved.
			// Previously an if/else-if pair whose second branch's
			// own `assertErr == nil` guard was only ever reachable when
			// FileExists had already returned true (the correct outcome, but
			// easy to misread as a fallthrough); restructured as a single
			// guarded block with no behaviour change.
			if assertErr == nil {
				if !w.env.FileExists(probeTokenFileName) {
					assertErr = fmt.Errorf("(b) token file: %s does not exist in the bind-mounted project dir after the run", probeTokenFileName)
				} else if got, rerr := w.env.ReadFile(probeTokenFileName); rerr != nil {
					assertErr = fmt.Errorf("(b) token file: exists but could not be read: %w", rerr)
				} else if !strings.Contains(got, res.Token) {
					assertErr = fmt.Errorf("(b) token file: content does not carry the probe's token")
				}
			}
		}

		if assertErr != nil {
			printProbeReport(engine, p.Axis, res.AuthPath, res.AuthReason, "FAILED", ": "+assertErr.Error())
			return assertErr
		}
		printProbeReport(engine, p.Axis, res.AuthPath, res.AuthReason, "PASSED", "")
		return nil
	})
}
