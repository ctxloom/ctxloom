package launch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// The isolation axes are core value types: two INDEPENDENT enums (where the
// files live, where the process runs), each with exactly one parser, and the
// Axes pair with the three predicates every "did we keep the boundary?"
// check asks. Values are the config/flag spellings and cannot change without
// every config file changing with them.

func TestWorkspaceAxis_Values_AreTheConfigSpellings(t *testing.T) {
	assert.Equal(t, WorkspaceAxis("none"), WorkspaceNone)
	assert.Equal(t, WorkspaceAxis("worktree"), WorkspaceWorktree)
	assert.Equal(t, []string{"none", "worktree"}, WorkspaceNames())
}

func TestParseWorkspaceAxis_EmptyPassesThrough_UnknownIsAnError(t *testing.T) {
	for _, s := range []string{"", "none", "worktree"} {
		got, err := ParseWorkspaceAxis(s)
		require.NoError(t, err, s)
		assert.Equal(t, WorkspaceAxis(s), got)
	}
	_, err := ParseWorkspaceAxis("wroktree")
	require.Error(t, err, "a typo must not read as the shared checkout")
	assert.Contains(t, err.Error(), "wroktree")
}

func TestRuntimeAxis_Values_AreTheConfigSpellings(t *testing.T) {
	assert.Equal(t, RuntimeAxis("host"), RuntimeHost)
	assert.Equal(t, RuntimeAxis("container-rootless"), RuntimeRootless)
	assert.Equal(t, RuntimeAxis("container-rootful"), RuntimeRootful)
	assert.Equal(t, []string{"host", "container-rootless", "container-rootful"}, RuntimeNames())
}

func TestParseRuntimeAxis_EmptyPassesThrough_UnknownIsAnError(t *testing.T) {
	for _, s := range []string{"", "host", "container-rootless", "container-rootful"} {
		got, err := ParseRuntimeAxis(s)
		require.NoError(t, err, s)
		assert.Equal(t, RuntimeAxis(s), got)
	}
	for _, s := range []string{"container", "docker", "hots"} {
		_, err := ParseRuntimeAxis(s)
		require.Error(t, err, "%q must be refused: an unrecognised runtime would land on the host", s)
	}
}

func TestIsContainerRuntimeAxis_EitherOwnershipMode(t *testing.T) {
	assert.True(t, IsContainerRuntimeAxis(RuntimeRootless))
	assert.True(t, IsContainerRuntimeAxis(RuntimeRootful))
	assert.False(t, IsContainerRuntimeAxis(RuntimeHost))
	assert.False(t, IsContainerRuntimeAxis(""))
	assert.True(t, IsContainerRuntime("container-rootful"), "the string-typed twin agrees")
}

func TestAxes_Predicates(t *testing.T) {
	assert.True(t, Axes{}.Zero())
	assert.True(t, Axes{Workspace: WorkspaceNone, Runtime: RuntimeHost}.Zero())
	assert.True(t, Axes{Workspace: WorkspaceWorktree}.WantsWorktree())
	assert.False(t, Axes{Workspace: WorkspaceWorktree}.WantsContainer())
	assert.True(t, Axes{Runtime: RuntimeRootless}.WantsContainer())
	assert.False(t, Axes{Runtime: RuntimeRootless}.Zero())
}

func TestDirtyTreeHandler_Values_AndParse(t *testing.T) {
	assert.Equal(t, []string{"commit", "copy", "stale", "fail"}, DirtyTreeHandlerNames())
	for _, s := range []string{"", "commit", "copy", "stale", "fail"} {
		got, err := ParseDirtyTreeHandler(s)
		require.NoError(t, err, s)
		assert.Equal(t, DirtyTreeHandler(s), got)
	}
	// The default member commits the user's tree, so anything that is not
	// exactly a member stops the spawn — a typo, a case variant, stray
	// whitespace, a value from another vocabulary — and the refusal quotes
	// what the caller actually typed and names the legal set.
	for _, bad := range []string{"comit", "fial", "COMMIT", " commit", "commit ", "true", "none"} {
		got, err := ParseDirtyTreeHandler(bad)
		require.Error(t, err, "%q is not a member", bad)
		assert.Equal(t, DirtyTreeHandler(""), got, "a refused parse yields no handler at all, least of all the default")
		assert.Contains(t, err.Error(), bad)
		assert.Contains(t, err.Error(), "commit|copy|stale|fail")
	}
}

func TestSource_CarriesWhatACallerKnows(t *testing.T) {
	// The verbatim shape from the decided design: every way a launch is asked
	// for is one of these. Nothing resolves it yet.
	src := Source{
		Identity:   sessions.Identity{Harp: "quiet-amber-falcon"},
		Agent:      "dev",
		Profiles:   []string{"base"},
		Label:      "fast",
		Mode:       engine.Interactive,
		Prompt:     "x",
		WorkDir:    "/p",
		Workspace:  WorkspaceWorktree,
		DirtyTree:  DirtyTreeHandlerCommit,
		Permission: "plan",
		Resume:     Resume{Ref: sessions.ResumeRef{Harp: "quiet-amber-falcon", NativeKey: "k"}, RebindEndpoint: true},
		Degraded:   false,
	}
	assert.Equal(t, "dev", src.Agent)
	assert.True(t, src.Resume.RebindEndpoint)
	assert.Equal(t, ImageConfig{}, ImageConfig{Image: ""}, "the zero ImageConfig means the engine's defaults")
}
