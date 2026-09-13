package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestContainerMount_CredentialMountIsReadWriteAtEveryDelegationDepth pins
// credential PARITY across the delegation tree (RULED: full parity at every
// depth, no trust gate): a delegated agent's container is authenticated
// exactly like the session owner's — the host's REAL subscription credential,
// bind-mounted READ-WRITE into the fresh container HOME — whether the run is
// the owner (depth 0), a child (depth 1) or a grandchild (depth 2).
//
// Depth is not an input to this package at all: Prepare takes axes, backend,
// image, project dir, agent id and session state, and the credential resolver
// (resolveClaudeContainerAuth) sees only the host env and the container home.
// So the test varies everything that DOES differ between an owner's run and a
// delegated child's — the agent id, the session harp, and the run env the
// coordinator stamps a child with (CTXLOOM_RUN_DEPTH, a literal here because
// internal/agentcoord/coord imports this package and cannot be imported back)
// — and asserts the credential mount in the rendered MountPlan is the same
// rw mount of the same host file every time. A future gate that keyed the
// mount's ReadOnly, its presence, or its source on any of those inputs turns
// this red.
//
// It runs against a real MountPlan (ResolveWorkspace → Mount through the
// real claude-code spec's resolver, a fake runtime and a stubbed shared-fs
// probe), on BOTH container bases: the host base (the owner's usual shape)
// and the worktree base (a delegated member's usual shape). What it cannot
// prove is the mount as the daemon realizes it — that needs a running
// container, and only the acceptance suite has one.
func TestContainerMount_CredentialMountIsReadWriteAtEveryDelegationDepth(t *testing.T) {
	home := testsupport.Isolate(t)
	// No env auth: the resolver must take the credential-MOUNT branch, not
	// passthrough. presentEnvKeys treats "" as unset.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	ctx := context.Background()

	realCreds := filepath.Join(home, ".claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(realCreds), 0o755))
	require.NoError(t, os.WriteFile(realCreds,
		[]byte(`{"claudeAiOauth":{"accessToken":"at","refreshToken":"single-use-rotating-rt"}}`), 0o600))
	// The personal top-level config exists on the host so the test also proves
	// it is NOT what crosses at any depth (claudeCredentialMounts' doc).
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"private":{}}}`), 0o600))

	// The REAL claude-code spec — its resolver is the code under test. A stub
	// resolver here would make every assertion below about the stub.
	spec := engineContainerSpecFor("claude-code")
	require.NotNil(t, spec.resolveAuth, "guard: the claude-code spec must carry a resolver, or nothing below exercises auth")

	fake := t.TempDir()
	script := filepath.Join(fake, "fake-docker")
	const image = "ctxloom-agent-depth-parity-test:latest"
	container := func(base containerBase) Container {
		return Container{
			runtime:    fakeRuntime{name: "docker", binary: script, available: true},
			image:      image,
			engine:     "claude-code",
			engineSpec: spec,
			binaryPath: defaultContainerBinary,
			home:       defaultContainerHome,
			socketDir:  defaultContainerSocketDir,
			base:       base,
		}
	}
	// The fake runtime reports the image present AND current: its label is the
	// same digest the image gate will compute for these containers, so the
	// gate takes the run-as-is path rather than a fake rebuild.
	probe := container(hostBase{})
	_, devBase, _ := probe.containerBuildSources("")
	labels := fmt.Sprintf(`{"ctxloom.provenance":%q}`, probe.provenanceFor(devBase))
	writeFakeRuntimeScript(t, script, filepath.Join(fake, "builds.log"), fake, labels)
	require.NoError(t, os.WriteFile(filepath.Join(fake, "ctxloom-agent-depth-parity-test_latest"), nil, 0o644))

	prevFS := sharedFSCheck
	sharedFSCheck = func(context.Context, Runtime, string, []string) error { return nil }
	t.Cleanup(func() { sharedFSCheck = prevFS })

	wantContainerPath := filepath.Join(defaultContainerHome, ".claude", ".credentials.json")

	bases := []struct {
		name string
		base containerBase
	}{
		{"host-base", hostBase{}},
		{"worktree-base", worktreeBase{wt: NewWorktree(&git.Fake{})}},
	}
	// One (harp, agent id) pair per depth: the identity a run at that depth
	// actually carries into Prepare. Distinct on purpose — identical inputs
	// would make the parity assertion vacuous.
	depths := []struct {
		depth   int
		harp    string
		agentID string
	}{
		{0, "brisk-teal-otter", "owner"},
		{1, "quiet-amber-wren", "child-a"},
		{2, "faint-coral-newt", "grandchild-a1"},
	}

	for _, b := range bases {
		t.Run(b.name, func(t *testing.T) {
			var seen []Mount
			for _, d := range depths {
				t.Run("depth-"+strconv.Itoa(d.depth), func(t *testing.T) {
					t.Setenv("CTXLOOM_RUN_DEPTH", strconv.Itoa(d.depth))

					chain := withSessionState([]Policy{container(b.base)}, SessionState{Harp: d.harp, ProjectID: "proj-1"})
					pol := chain[0].(Container)

					ws, err := pol.ResolveWorkspace(ctx, t.TempDir(), d.agentID)
					require.NoError(t, err, "the container gate must pass: runtime up, image present, creds resolvable")
					t.Cleanup(func() { _ = ws.Cleanup() })

					plan, err := pol.Mount(ctx, ws)
					require.NoError(t, err)
					require.NotEmpty(t, plan.Mounts, "guard: Mount must render mounts, or the credential search below is trivially empty")

					var creds []Mount
					for _, m := range plan.Mounts {
						if m.Host == realCreds {
							creds = append(creds, m)
						}
						assert.NotEqual(t, filepath.Join(home, ".claude.json"), m.Host,
							"the personal top-level config never crosses, at any depth")
					}
					require.Len(t, creds, 1, "exactly one mount carries the host credential at depth %d", d.depth)
					got := creds[0]

					// THE ASSERTIONS THIS TEST EXISTS FOR.
					assert.False(t, got.ReadOnly,
						"depth %d: the credential mount is READ-WRITE — the in-container token refresh must land in the one real host file, or it rotates the host's single-use token out from under it", d.depth)
					assert.Equal(t, wantContainerPath, got.Container,
						"depth %d: the credential lands where claude looks in the fresh container HOME", d.depth)
					assert.Equal(t, realCreds, got.Host,
						"depth %d: the source is the REAL host credential, not a staged copy", d.depth)
					assert.Equal(t, authCredentialMount, ws.(*containerWorkspace).authMode,
						"depth %d: the plan was resolved through the credential-mount branch, not env passthrough", d.depth)

					seen = append(seen, got)
				})
			}
			require.Len(t, seen, len(depths), "guard: every depth produced a credential mount to compare")
			for i := 1; i < len(seen); i++ {
				assert.Equal(t, seen[0], seen[i],
					"PARITY: depth %d's credential mount is byte-identical to depth 0's — no trust gate distinguishes a delegated agent from the owner", depths[i].depth)
			}
		})
	}
}
