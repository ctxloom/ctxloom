package isolation

import (
	"context"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

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

// Approvals keeps the engine's in-tool approval prompt (host session, today's
// behaviour).
func (None) Approvals() Approvals { return ApprovalsPrompt }

// PrepareWorkspace returns the live project directory as the workspace with a
// noop cleanup — there is nothing to provision or tear down.
func (None) PrepareWorkspace(_ context.Context, projectDir, _ string) (Workspace, error) {
	return hostWorkspace{dir: projectDir}, nil
}

// SpawnClient launches the bare self-invoked plugin subprocess via the Host
// runtime (the exact body of pb.DefaultClientFactory). The workspace is
// expressed purely via the caller's RunOptions.WorkDir, so no per-workspace
// launch machinery is needed here.
func (None) SpawnClient(backendName, label string, verbosity int, _ Workspace, spawnEnv map[string]string) (pb.Client, error) {
	return Host{}.Spawn(LaunchSpec{BackendName: backendName, Label: label, Verbosity: verbosity, SpawnEnv: spawnEnv})
}

// hostWorkspace is the None policy's workspace: the live project directory with
// no teardown.
type hostWorkspace struct{ dir string }

// Dir returns the project directory.
func (w hostWorkspace) Dir() string { return w.dir }

// Cleanup is a noop — the live project directory is never torn down.
func (hostWorkspace) Cleanup() error { return nil }
