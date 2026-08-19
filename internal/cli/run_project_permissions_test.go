package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The PROJECT DEFAULT posture (config.yaml's top-level `permissions:`) is the
// fourth rung of the run's resolution chain:
//
//	--permissions flag > --agent binding > engine label > PROJECT DEFAULT > engine built-in
//
// The two properties that matter are the two ends of that sentence. Anything
// NARROWER that someone actually declared — a flag, a binding, a label — always
// wins, so a project default can never quietly widen a posture a human wrote
// down somewhere more specific. And a DECLARED project default beats the engine
// built-in, including the claude-code host bypass stopgap: the stopgap exists
// precisely because nobody stated a posture, so a project that states one has
// answered the question the stopgap was standing in for.

// TestResolvePermissionMode_ProjectDefault pins that chain position directly on
// the resolver.
//
// MUTATION TARGET (m2): moving projectPerm ahead of labelPerm/agentPerm/flag in
// resolvePermissionMode's source list turns every "beats the project default"
// row below red.
func TestResolvePermissionMode_ProjectDefault(t *testing.T) {
	const claude = config.DefaultLLM // "claude-code"
	cases := []struct {
		name         string
		flag         string
		agentPerm    string
		labelPerm    string
		projectPerm  string
		backend      string
		mode         pb.ExecutionMode
		enforcesPlan bool
		want         agent.PermissionMode
	}{
		// The rung itself: nothing narrower declared, so the project default is
		// what the run launches at — on a NON-claude backend (no stopgap in
		// play) this is the plain "the project widened its own dir" case.
		{"project default is honored when nothing narrower is declared", "", "", "", "bypass", "codex", pb.ExecutionMode_INTERACTIVE, true, agent.PermissionBypass},
		{"project default can also narrow", "", "", "", "plan", "codex", pb.ExecutionMode_INTERACTIVE, true, agent.PermissionPlan},

		// Narrower always wins — one row per rung above the project default.
		{"label beats the project default", "", "", "plan", "bypass", "codex", pb.ExecutionMode_INTERACTIVE, true, agent.PermissionPlan},
		{"agent binding beats the project default", "", "plan", "", "bypass", "codex", pb.ExecutionMode_INTERACTIVE, true, agent.PermissionPlan},
		{"flag beats the project default", "plan", "", "", "bypass", "codex", pb.ExecutionMode_INTERACTIVE, true, agent.PermissionPlan},
		// ...and an explicitly-declared WIDER posture above still wins too: the
		// rule is precedence, not "most restrictive". A project that pinned plan
		// has not taken away a binding's right to declare bypass for itself.
		{"a wider label still beats a narrow project default", "", "", "bypass", "plan", "codex", pb.ExecutionMode_INTERACTIVE, true, agent.PermissionBypass},

		// The stopgap interaction, both directions.
		{"a declared project default beats the claude-code host stopgap", "", "", "", "plan", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionPlan},
		{"an explicit project default:default opts out of the stopgap", "", "", "", "default", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionDefault},
		{"an undeclared project default leaves the stopgap standing", "", "", "", "", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionBypass},
		{"an undeclared project default leaves a non-claude backend prompting", "", "", "", "", "codex", pb.ExecutionMode_INTERACTIVE, true, agent.PermissionDefault},

		// A hand-edited misspelling is refused as a fatal finding rather than
		// hard-failing the launch here (only the typed --permissions flag is
		// strict — validatePermissionFlag), and the posture it lands on is the
		// floor: falling THROUGH would hand the run whatever the rung below
		// happens to say, up to and including the claude-code host stopgap.
		{"an unparseable project default floors to read-only", "", "", "", "nonsense", "codex", pb.ExecutionMode_INTERACTIVE, true, agent.PermissionFloor},
		{"an unparseable project default floors on claude-code too", "", "", "", "nonsense", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionFloor},

		// The collapses/floors that sit downstream of the whole chain apply to a
		// project-sourced posture exactly as to any other.
		{"a project plan collapses on a backend with no read-only tier", "", "", "", "plan", "antigravity", pb.ExecutionMode_INTERACTIVE, false, agent.PermissionDefault},
		{"a project default floors up for a headless oneshot", "", "", "", "default", "codex", pb.ExecutionMode_ONESHOT, true, agent.PermissionBypass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePermissionMode(tc.flag, tc.agentPerm, tc.labelPerm, tc.projectPerm, tc.backend, tc.mode, tc.enforcesPlan)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestRequestedPermission_IncludesProjectDefault: a declared project default IS
// a requested posture, so the "this backend cannot honor what you asked for"
// warnings fire for it too. A project that pinned `plan` on a backend with no
// read-only tier must be told its pin collapsed — that is the same silent-
// widening the warning exists to prevent, and it does not become acceptable
// because the declaration lived in the project file rather than on a binding.
func TestRequestedPermission_IncludesProjectDefault(t *testing.T) {
	m, ok := requestedPermission("", "", "", "plan")
	assert.True(t, ok, "a declared project default is a request")
	assert.Equal(t, agent.PermissionPlan, m)

	m, ok = requestedPermission("", "", "bypass", "plan")
	assert.True(t, ok)
	assert.Equal(t, agent.PermissionBypass, m, "the label is nearer than the project default")

	_, ok = requestedPermission("", "", "", "")
	assert.False(t, ok, "nothing requested")

	_, ok = requestedPermission("", "", "", "nonsense")
	assert.False(t, ok, "an unparseable project default is not a request")
}

// newPermissionRunState builds the minimum runState buildRunRequest reads, so
// the assertion below lands on the RunStart payload that actually rides the
// wire rather than on the resolver's return value in isolation.
func newPermissionRunState(t *testing.T, cfg *config.Config, label, backend string) *runState {
	t.Helper()
	return &runState{
		cfg:         cfg,
		label:       label,
		backendName: backend,
		mode:        pb.ExecutionMode_INTERACTIVE,
		workDir:     t.TempDir(),
		ctxResult:   &operations.AssembleContextResult{},
	}
}

// TestBuildRunRequest_HonorsProjectPermissionDefault is the end-to-end payload
// assertion for the bare run: a project config declaring `permissions: bypass`
// and NOTHING else must put "bypass" on the wire. Asserting on st.req.Options
// rather than on resolvePermissionMode's return is deliberate — the resolver
// being right is worth nothing if buildRunRequest never hands it the project
// value, which is exactly the wiring this feature adds.
func TestBuildRunRequest_HonorsProjectPermissionDefault(t *testing.T) {
	t.Run("declared project default rides the request", func(t *testing.T) {
		resetStrictness(t)
		withRunPermissionsFlag(t, "")

		cfg := config.NewFixture(config.Fixture{
			AppPaths:    []string{t.TempDir()},
			Permissions: "bypass",
		})
		st := newPermissionRunState(t, cfg, "codex", "codex")
		st.buildRunRequest()

		require.NotNil(t, st.req)
		require.NotNil(t, st.req.Options)
		assert.Equal(t, agent.PermissionBypass.String(), st.req.Options.PermissionMode,
			"a bare run in a project that declared `permissions: bypass` must launch at bypass")
	})

	// The narrower-wins half, driven through the SAME payload path: the engine
	// label declares plan while the project declares bypass. The nearer, more
	// specific declaration wins and the project default never widens it.
	t.Run("a label posture beats the project default on the wire", func(t *testing.T) {
		resetStrictness(t)
		withRunPermissionsFlag(t, "")

		cfg := config.NewFixture(config.Fixture{
			AppPaths:    []string{t.TempDir()},
			Permissions: "bypass",
			LM: config.LMConfig{Configs: map[string]config.LLMConfig{
				"careful": {Type: "codex", Permissions: "plan"},
			}},
		})
		st := newPermissionRunState(t, cfg, "careful", "codex")
		st.buildRunRequest()

		require.NotNil(t, st.req)
		require.NotNil(t, st.req.Options)
		assert.Equal(t, agent.PermissionPlan.String(), st.req.Options.PermissionMode,
			"the engine label's declared plan must beat the project default's bypass — a project default may never widen a narrower declaration")
	})

	// Undeclared: the project contributes nothing and the engine's own built-in
	// default stands, byte-identical to the behaviour before this key existed.
	t.Run("an undeclared project default changes nothing", func(t *testing.T) {
		resetStrictness(t)
		withRunPermissionsFlag(t, "")

		cfg := config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})
		st := newPermissionRunState(t, cfg, "codex", "codex")
		st.buildRunRequest()

		require.NotNil(t, st.req)
		require.NotNil(t, st.req.Options)
		assert.Equal(t, agent.PermissionDefault.String(), st.req.Options.PermissionMode,
			"a project that declares no posture must leave the engine's own default exactly where it was")
	})
}

// withRunPermissionsFlag sets the package-level --permissions flag var for one
// test and restores it after: buildRunRequest reads it directly, and a value
// left behind by another test would silently override the chain under test.
func withRunPermissionsFlag(t *testing.T, v string) {
	t.Helper()
	orig := runPermissions
	runPermissions = v
	t.Cleanup(func() { runPermissions = orig })
}
