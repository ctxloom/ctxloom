package operations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// doctorApp scaffolds a hermetic project the way `ctxloom manage install`
// does and opens the composition over it, with the host's ssh-agent and git
// identity out of reach so the two machine probes never read this machine.
func doctorApp(t *testing.T) (*App, string) {
	t.Helper()
	home := testsupport.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	_, err := InitializeProject(context.Background(), engines.Registry(), InitializeProjectRequest{AppDir: appDir, Engine: "claude-code"})
	require.NoError(t, err)
	return testApp(t, configload.WithAppDir(appDir)), home
}

func doctorMarkers(rep DoctorReport) []string {
	out := make([]string, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		out = append(out, c.Marker)
	}
	return out
}

// TestDoctor_DepsOnly_ReportsTheMachineProbesInOrder: --deps is the
// machine-capability scope init's PRIME runs before a project exists — the
// three probes, nothing about agents, hooks or trust.
func TestDoctor_DepsOnly_ReportsTheMachineProbesInOrder(t *testing.T) {
	app, home := doctorApp(t)
	rep, err := Doctor(context.Background(), app, DoctorRequest{DepsOnly: true, Home: home})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"DOCTOR-CHECK-DEPS-a1",
		"DOCTOR-CHECK-SIGNKEY-k1",
		"DOCTOR-CHECK-GITIDENT-l2",
	}, doctorMarkers(rep))
}

// TestDoctor_FullReport_RunsEveryCheckInItsFixedOrder pins the report's
// order, which is the order the CLI renders and the order every consumer of
// `doctor --format json` has seen: a check that moves, appears twice or
// disappears fails here as a record, not as a rendered line.
func TestDoctor_FullReport_RunsEveryCheckInItsFixedOrder(t *testing.T) {
	app, home := doctorApp(t)
	rep, err := Doctor(context.Background(), app, DoctorRequest{Home: home})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"DOCTOR-CHECK-SETUP-MARKER-e5",
		"DOCTOR-CHECK-DEPS-a1",
		"DOCTOR-CHECK-SIGNKEY-k1",
		"DOCTOR-CHECK-GITIDENT-l2",
		"DOCTOR-CHECK-AGENTS-b2",
		"DOCTOR-CHECK-CAPABILITY-LOSS-u1",
		"DOCTOR-CHECK-VERSION-c3",
		"DOCTOR-CHECK-TRANSCRIPT-READER-v2",
		"DOCTOR-CHECK-HOOKS-TRUST-d4",
		"DOCTOR-CHECK-MCP-INVOCATION-g7",
		"DOCTOR-CHECK-APPROVALS-STORE-a2",
		"DOCTOR-CHECK-CONTENT-TRUST-n4",
		"DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5",
		"DOCTOR-CHECK-SETUP-DEPS-h8",
		"DOCTOR-CHECK-SETUP-COMPANIONS-i9",
		"DOCTOR-CHECK-SETUP-AUTHPING-j0",
		"DOCTOR-CHECK-INGESTION-q7",
		"DOCTOR-CHECK-LOCAL-STATE-p6",
		"DOCTOR-CHECK-GITIGNORE-f6",
		"DOCTOR-CHECK-FOREIGN-WORKTREES-r8",
		"DOCTOR-CHECK-ORPHAN-CONTAINERS-z2",
		"DOCTOR-CHECK-SUPERSEDED-IMAGES-x4",
		"DOCTOR-CHECK-LEGACY-INDEX-y5",
		"DOCTOR-CHECK-HARP-DURABILITY-s9",
		"DOCTOR-CHECK-SPOOL-BACKLOG-t0",
		"DOCTOR-CHECK-SPOOL-COUNTERS-w3",
		"DOCTOR-CHECK-TTY-INJECTION-m3",
	}, doctorMarkers(rep))
	for _, c := range rep.Checks {
		assert.Containsf(t, []DoctorStatus{DoctorOK, DoctorWarn, DoctorInfo}, c.Status, "%s carries a status outside the vocabulary", c.Marker)
		assert.NotEmptyf(t, c.Detail, "%s reports no detail", c.Marker)
	}
}

// TestDoctor_ProbesEachRuntimeOnce: every runtime row reads one shared
// reachability probe. A probe is an `info` round trip to the engine — seconds
// for podman, up to runtimeProbeTimeout for a wedged one — so a report that
// re-probed per row multiplied that wait by the rows. The fakes answer every
// call and log it; a reachable runtime is then probed exactly once.
func TestDoctor_ProbesEachRuntimeOnce(t *testing.T) {
	app, home := doctorApp(t)
	dir := t.TempDir()
	for _, bin := range []string{"docker", "podman"} {
		script := "#!/bin/sh\necho \"$*\" >> " + filepath.Join(dir, bin+".calls") + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o755))
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := Doctor(context.Background(), app, DoctorRequest{Home: home})
	require.NoError(t, err)

	for _, bin := range []string{"docker", "podman"} {
		raw, err := os.ReadFile(filepath.Join(dir, bin+".calls"))
		require.NoError(t, err, "%s was never probed", bin)
		probes := 0
		for _, line := range strings.Split(string(raw), "\n") {
			if line == "info" {
				probes++
			}
		}
		assert.Equal(t, 1, probes, "%s's reachability probes:\n%s", bin, raw)
	}
}
