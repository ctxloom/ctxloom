package isolation

import (
	"context"
	"fmt"
	"os/exec"
)

// startHostRunner is StartRunner's seam onto the bare self-invoked host runner
// spawn, a package var so the launch-failure path is unit-testable without
// actually forking a `ctxloom runner` subprocess. Mirrors the
// selectRuntimeProbe / sharedFSCheck seams.
var startHostRunner = StartHostRunner

// None is the default, host isolation policy. The workspace IS the live
// project directory (no worktree, no container), its cleanup is a noop, and
// the runner is a bare self-invoked `ctxloom runner` subprocess. It is the
// fault-tolerant floor: None never fails to prepare a workspace or start a
// runner, so a run always has a working policy to fall back to.
type None struct{}

// Ensure None satisfies the policy interface.
var _ policy = None{}

// Name returns the policy identifier.
func (None) Name() string { return "none" }

// ResolveWorkspace returns the live project directory as the workspace with a
// noop cleanup — there is nothing to materialize or tear down.
func (None) resolveWorkspace(_ context.Context, projectDir, _ string) (workspace, error) {
	return hostWorkspace{dir: projectDir}, nil
}

// mount maps nothing. A host run's engine executes IN the workspace directory,
// so there is no second environment to map it into and no plan to render — the
// empty plan is the complete answer for this policy, not an unimplemented stub.
func (None) bind(context.Context, workspace) (mountPlan, error) { return mountPlan{}, nil }

// PrepareWorkspace resolves and maps in one step (see prepareWorkspace).
func (n None) prepareWorkspace(ctx context.Context, projectDir, agentID string) (workspace, error) {
	return resolveAndBind(ctx, n, projectDir, agentID)
}

// StartRunner launches the bare self-invoked `ctxloom runner <engine>`
// subprocess — the host StartRun spawn half.
// The runner learns its cell from the Launch the coordinator's StartRun
// carries, so the per-spawn env is the reach-back trio the caller built and
// nothing more. verbosity is
// ambient (the runner reads CTXLOOM_VERBOSE), so it does not ride the argv.
// Readiness is the coordinator's awaitRunner, not observed here. ctx is
// checked once, before the spawn: an already-cancelled launch returns
// ctx.Err() and starts nothing; after that, teardown is Kill, never ctx.
func (None) startRunner(ctx context.Context, backendName, label string, _ int, _ workspace, spawnEnv map[string]string) (*RunnerHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	env := make(map[string]string, len(spawnEnv))
	for k, v := range spawnEnv {
		env[k] = v
	}
	// The runner reads no config: the label body rides the Launch, so the
	// label never reaches its argv.
	hr, err := startHostRunner([]string{"runner", backendName}, env)
	if err != nil {
		return nil, fmt.Errorf("start host runner for backend %q (label %q): %w", backendName, label, err)
	}
	return &RunnerHandle{Name: "", Kill: hr.Kill, Wait: hr.Wait, StderrTail: hr.StderrTail}, nil
}

// InteractiveRunner is the self-exec'd runner on the host: the originator
// starts it on the pty it holds.
func (None) interactiveRunner(_ context.Context, backendName string, _ workspace, spawnEnv map[string]string) (*exec.Cmd, string, error) {
	return RunnerCommand(backendName, spawnEnv), "", nil
}

// hostWorkspace is the None policy's Workspace: the live project directory with
// no teardown.
type hostWorkspace struct{ dir string }

// Dir returns the project directory.
func (w hostWorkspace) Dir() string { return w.dir }

// Cleanup is a noop — the live project directory is never torn down.
func (hostWorkspace) Cleanup() error { return nil }
