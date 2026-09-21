package isolation

import (
	"context"
	"fmt"
	"os/exec"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// startHostRunner is StartRunner's seam onto the bare self-invoked host runner
// spawn, a package var so the launch-failure path is unit-testable without
// actually forking a `ctxloom llm host` subprocess. Mirrors the
// selectRuntimeProbe / sharedFSCheck seams.
var startHostRunner = pb.StartHostRunner

// None is the default, host isolation policy — behaviour-identical to today.
// The workspace IS the live project directory (no worktree, no container), its
// cleanup is a noop, the plugin is a bare self-invoked `ctxloom llm serve`
// subprocess (exactly pb.DefaultClientFactory), and approvals stay Prompt. It is
// the fault-tolerant floor: None never fails to prepare a workspace or spawn a
// client, so a run always has a working policy to fall back to.
type None struct{}

// Ensure None satisfies the Policy interface.
var _ Policy = None{}

// Name returns the policy identifier.
func (None) Name() string { return "none" }

// ResolveWorkspace returns the live project directory as the workspace with a
// noop cleanup — there is nothing to materialize or tear down.
func (None) ResolveWorkspace(_ context.Context, projectDir, _ string) (Workspace, error) {
	return hostWorkspace{dir: projectDir}, nil
}

// Mount maps nothing. A host run's engine executes IN the workspace directory,
// so there is no second environment to map it into and no plan to render — the
// empty plan is the complete answer for this policy, not an unimplemented stub.
func (None) Mount(context.Context, Workspace) (MountPlan, error) { return MountPlan{}, nil }

// PrepareWorkspace resolves and maps in one step (see prepareWorkspace).
func (n None) PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error) {
	return prepareWorkspace(ctx, n, projectDir, agentID)
}

// SpawnClient launches the bare self-invoked plugin subprocess via the Host
// runtime (the exact body of pb.DefaultClientFactory). The workspace is
// expressed purely via the caller's RunOptions.WorkDir, so no per-workspace
// launch machinery is needed here.
func (None) SpawnClient(backendName, label string, verbosity int, ws Workspace, spawnEnv map[string]string) (pb.Client, error) {
	return Host{}.Spawn(LaunchSpec{BackendName: backendName, Label: label, Verbosity: verbosity, SpawnEnv: spawnEnv})
}

// StartRunner launches the bare self-invoked `ctxloom runner <engine>`
// subprocess — the host StartRun spawn half.
// The runner learns its cell from the Launch the coordinator's StartRun
// carries, so the per-spawn env is the reach-back trio the caller built and
// nothing more. verbosity is
// ambient (the runner reads CTXLOOM_VERBOSE), so it does not ride the argv.
// Readiness is the coordinator's awaitRunner, not observed here.
func (None) StartRunner(_ context.Context, backendName, label string, _ int, _ Workspace, spawnEnv map[string]string) (*RunnerHandle, error) {
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
func (None) InteractiveRunner(_ context.Context, backendName string, _ Workspace, spawnEnv map[string]string) (*exec.Cmd, string, error) {
	return RunnerCommand(backendName, spawnEnv), "", nil
}

// hostWorkspace is the None policy's workspace: the live project directory with
// no teardown.
type hostWorkspace struct{ dir string }

// Dir returns the project directory.
func (w hostWorkspace) Dir() string { return w.dir }

// Cleanup is a noop — the live project directory is never torn down.
func (hostWorkspace) Cleanup() error { return nil }
