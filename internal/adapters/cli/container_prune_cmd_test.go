package cli

import (
	"bytes"
	"context"
	"errors"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

var pruneGoldenNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// pruneDryRunReport is a dry-run plan over both runtimes: every keep reason,
// one removal, one unowned look-alike, and a runtime with nothing to do.
func pruneDryRunReport() operations.ContainerPruneReport {
	return operations.ContainerPruneReport{
		MinAge: 24 * time.Hour,
		Runtimes: []operations.ContainerPruneRuntime{
			{
				Runtime: "docker",
				Images: []operations.ContainerPruneImage{
					{Ref: "ctxloom-agent-base:70e0a366129a", Action: operations.PruneKeep, Reason: isolation.KeepParent},
					{Ref: "ctxloom-agent-claude-code:v0.7.0-1c7b279-cbf9-b236", Action: operations.PruneKeep, Reason: isolation.KeepCurrent},
					{Ref: "ctxloom-agent-mock:v0.7.0-05c964d-cce3-b236", Action: operations.PruneRemove, Created: pruneGoldenNow.Add(-50 * time.Hour), Bytes: 42_100_000},
					{Ref: "ctxloom-agent-mock:v0.7.0-1c7b279-cce3-b236", Action: operations.PruneKeep, Reason: isolation.KeepNewestInSlot},
					{Ref: "ctxloom-agent-mock:v0.7.0-a68169e-cce3-b236", Action: operations.PruneKeep, Reason: isolation.KeepYoung},
					{Ref: "ctxloom-agent-mock:v0.7.0-d00d00d-cce3-b236", Action: operations.PruneKeep, Reason: isolation.KeepReferenced},
				},
				Unowned: []string{"ctxloom-agent-base:latest"},
				Bytes:   42_100_000,
			},
			{Runtime: "podman"},
		},
	}
}

const pruneDryRunGolden = `docker
  KEEP    ctxloom-agent-base:70e0a366129a  base of a kept image
  KEEP    ctxloom-agent-claude-code:v0.7.0-1c7b279-cbf9-b236  current identity
  REMOVE  ctxloom-agent-mock:v0.7.0-05c964d-cce3-b236  superseded (2d old, 42.1 MB)
  KEEP    ctxloom-agent-mock:v0.7.0-1c7b279-cce3-b236  newest in slot
  KEEP    ctxloom-agent-mock:v0.7.0-a68169e-cce3-b236  younger than 24h0m0s
  KEEP    ctxloom-agent-mock:v0.7.0-d00d00d-cce3-b236  used by a container
  SKIP    ctxloom-agent-base:latest  unowned (no ctxloom label)
  1 to remove, 42.1 MB reclaimable — dry run; pass --apply to remove
podman
  nothing to remove
`

// TestRenderContainerPrune_DryRunGolden pins the dry-run text report.
func TestRenderContainerPrune_DryRunGolden(t *testing.T) {
	orig := pruneNow
	pruneNow = func() time.Time { return pruneGoldenNow }
	t.Cleanup(func() { pruneNow = orig })

	var out bytes.Buffer
	require.NoError(t, renderContainerPrune(&out, pruneDryRunReport()))
	assert.Equal(t, pruneDryRunGolden, out.String())
}

// TestRenderContainerPrune_AppliedWithFailure: an applied sweep lists what it
// removed and what the runtime refused, with the runtime's reason.
func TestRenderContainerPrune_AppliedWithFailure(t *testing.T) {
	rep := operations.ContainerPruneReport{Applied: true, MinAge: time.Hour, Runtimes: []operations.ContainerPruneRuntime{{
		Runtime: "docker",
		Images: []operations.ContainerPruneImage{
			{Ref: "a:1", Action: operations.PruneRemoved, Created: pruneGoldenNow.Add(-3 * time.Hour), Bytes: 1000},
			{Ref: "a:2", Action: operations.PruneFailed, Error: "image is being used"},
		},
		Bytes: 1000,
	}}}
	orig := pruneNow
	pruneNow = func() time.Time { return pruneGoldenNow }
	t.Cleanup(func() { pruneNow = orig })
	var out bytes.Buffer
	require.NoError(t, renderContainerPrune(&out, rep))
	assert.Equal(t, `docker
  REMOVED a:1  superseded (3h old, 1.0 kB)
  FAILED  a:2  removal failed: image is being used
  1 removed, 1.0 kB reclaimed; 1 failed
`, out.String())
}

// stubContainerPrune scripts the prune service for one CLI run and resets the
// command's flags, which are package state shared across tests.
func stubContainerPrune(t *testing.T, fn func(context.Context, *operations.App, operations.ContainerPruneRequest) (operations.ContainerPruneReport, error)) *operations.ContainerPruneRequest {
	t.Helper()
	formatCoverageProject(t)
	var got operations.ContainerPruneRequest
	orig := containerPrune
	containerPrune = func(ctx context.Context, app *operations.App, req operations.ContainerPruneRequest) (operations.ContainerPruneReport, error) {
		got = req
		return fn(ctx, app, req)
	}
	reset := func() {
		containerPruneApply, containerPruneMinAge, containerPruneRuntime = false, operations.DefaultImagePruneMinAge, ""
		for _, name := range []string{"apply", "min-age", "runtime"} {
			containerPruneCmd.Flags().Lookup(name).Changed = false
		}
	}
	reset()
	t.Cleanup(func() { containerPrune = orig; reset() })
	return &got
}

// TestContainerPrune_ExitCodes: 0 for a plan (even one with work to do) and
// for nothing to do, 1 when a removal failed or a runtime could not be
// planned, 3 when no runtime is available (the ClassIsolation finding).
func TestContainerPrune_ExitCodes(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		report   operations.ContainerPruneReport
		noRT     bool
		wantCode int
	}{
		{name: "dry-run plan with work to do", report: pruneDryRunReport(), wantCode: 0},
		{name: "nothing to do", report: operations.ContainerPruneReport{Runtimes: []operations.ContainerPruneRuntime{{Runtime: "docker"}}}, wantCode: 0},
		{name: "a removal failed", args: []string{"--apply"}, wantCode: 1, report: operations.ContainerPruneReport{Applied: true, Runtimes: []operations.ContainerPruneRuntime{{
			Runtime: "docker", Images: []operations.ContainerPruneImage{{Ref: "a:1", Action: operations.PruneFailed, Error: "boom"}},
		}}}},
		{name: "a runtime could not be planned", wantCode: 1, report: operations.ContainerPruneReport{Runtimes: []operations.ContainerPruneRuntime{{Runtime: "docker", Error: "images: boom"}}}},
		{name: "no runtime available", noRT: true, wantCode: exitCodeFatalFindings},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubContainerPrune(t, func(context.Context, *operations.App, operations.ContainerPruneRequest) (operations.ContainerPruneReport, error) {
				if tc.noRT {
					strictness.FailAlways(report.KindIsolation, "start one", "container prune: no container runtime is available to prune")
				}
				return tc.report, nil
			})
			_, err := runRoot(t, append([]string{"container", "prune"}, tc.args...)...)
			if tc.wantCode == 0 {
				assert.NoError(t, err)
				return
			}
			var ee *ExitError
			require.True(t, errors.As(err, &ee), "want exit %d, got %v", tc.wantCode, err)
			assert.Equal(t, tc.wantCode, ee.Code)
		})
	}
}

// TestContainerPrune_DryRunUnlessApply: the destructive mode is opt-in, and
// the flags reach the service as given.
func TestContainerPrune_DryRunUnlessApply(t *testing.T) {
	empty := func(context.Context, *operations.App, operations.ContainerPruneRequest) (operations.ContainerPruneReport, error) {
		return operations.ContainerPruneReport{}, nil
	}
	got := stubContainerPrune(t, empty)
	_, err := runRoot(t, "container", "prune")
	require.NoError(t, err)
	assert.False(t, got.Apply, "no --apply must never remove")
	assert.Equal(t, operations.DefaultImagePruneMinAge, got.MinAge)

	got = stubContainerPrune(t, empty)
	_, err = runRoot(t, "container", "prune", "--apply", "--min-age", "2h", "--runtime", "podman")
	require.NoError(t, err)
	assert.Equal(t, operations.ContainerPruneRequest{Apply: true, MinAge: 2 * time.Hour, Runtime: "podman"}, *got)
}
