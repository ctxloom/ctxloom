package isolation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// envMap parses a "KEY=VAL" env slice (the docker/podman `-e` form the container
// spawn consumes) into a lookup map so a test can assert on ACTUAL values rather
// than a pin or a count.
func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	m := make(map[string]string, len(env))
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		require.True(t, ok, "malformed env entry %q (want KEY=VAL)", e)
		m[k] = v
	}
	return m
}

// TestGitIdentityEnv_PerAgentEmail is the core isolation property: two DIFFERENT
// agentIDs derive DIFFERENT GIT_AUTHOR_EMAIL values (the actual addresses, not a
// count) — the whole point of scoping identity per agent so a containerized
// commit through the shared, RW-mounted .git common dir can never misattribute
// one agent's work to another.
func TestGitIdentityEnv_PerAgentEmail(t *testing.T) {
	a := envMap(t, gitIdentityEnv("agent-a"))
	b := envMap(t, gitIdentityEnv("agent-b"))

	assert.Equal(t, "agent-a@agents.ctxloom.local", a["GIT_AUTHOR_EMAIL"])
	assert.Equal(t, "agent-b@agents.ctxloom.local", b["GIT_AUTHOR_EMAIL"])
	assert.NotEqual(t, a["GIT_AUTHOR_EMAIL"], b["GIT_AUTHOR_EMAIL"],
		"different agents must not share a git author email — that is the misattribution bug")
}

// TestGitIdentityEnv_AllFourVars proves all four GIT_* vars are present and carry
// the SAME format the host+worktree path uses (worktreeWorkspace.Env → the shared
// gitIdentity helper): author and committer name/email mirror each other.
func TestGitIdentityEnv_AllFourVars(t *testing.T) {
	m := envMap(t, gitIdentityEnv("agent-a"))

	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		_, ok := m[k]
		assert.Truef(t, ok, "%s must be injected into the container env", k)
	}
	assert.Equal(t, "ctxloom agent agent-a", m["GIT_AUTHOR_NAME"])
	assert.Equal(t, m["GIT_AUTHOR_NAME"], m["GIT_COMMITTER_NAME"], "committer name mirrors author name")
	assert.Equal(t, m["GIT_AUTHOR_EMAIL"], m["GIT_COMMITTER_EMAIL"], "committer email mirrors author email")

	assert.Nil(t, gitIdentityEnv(""), "an empty agentID injects nothing — falls back to the container's own git config")
}

// TestContainerGitIdentity_ReachesSpawnEnv is the WIRING proof: the exact env the
// container spawn consumes (ExecSpec's RunSpec.Env, and launchSpec.ExtraEnv)
// carries the per-agent GIT_AUTHOR_EMAIL, and two workspaces built for different
// agentIDs land different emails there.
//
// It builds each containerWorkspace's extraEnv the SAME way PrepareWorkspace does
// (gitIdentityEnv appended onto the base run env), then reads the value back off
// the spawn spec. The NON-VACUOUS premise is a base sentinel entry: it can only
// appear in the spawn env if that env is actually assembled FROM cw.extraEnv — so
// if the assertion below found the git email while the sentinel was MISSING, the
// test would be reading a map the spawn never sees, and it fails.
func TestContainerGitIdentity_ReachesSpawnEnv(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "docker", available: true}, "mock").WithImage("img")

	build := func(agentID string) *containerWorkspace {
		// Mirror PrepareWorkspace's assembly: a base run env (here a sentinel
		// standing in for sc.runEnv()'s auth/term entries) plus the injected
		// per-agent git identity.
		base := []string{"CTXLOOM_TEST_SENTINEL=present"}
		return &containerWorkspace{
			dir:      "/proj",
			agentID:  agentID,
			extraEnv: append(base, gitIdentityEnv(agentID)...),
		}
	}

	cwA := build("agent-a")
	cwB := build("agent-b")

	// --- launchSpec (the go-plugin spawn path) ---
	specA := c.launchSpec("mock", "label", 0, cwA)
	require.Same(t, &cwA.extraEnv[0], &specA.ExtraEnv[0],
		"premise: launchSpec.ExtraEnv IS cw.extraEnv — the slice the spawn threads verbatim")
	launchA := envMap(t, specA.ExtraEnv)
	assert.Equal(t, "present", launchA["CTXLOOM_TEST_SENTINEL"], "premise: the base env reaches the spawn")
	assert.Equal(t, "agent-a@agents.ctxloom.local", launchA["GIT_AUTHOR_EMAIL"])

	// --- ExecSpec (the ISO1 direct-exec path) ---
	execA, err := c.ExecSpec(cwA, []string{"true"}, nil, nil)
	require.NoError(t, err)
	execB, err := c.ExecSpec(cwB, []string{"true"}, nil, nil)
	require.NoError(t, err)

	mA := envMap(t, execA.Env)
	mB := envMap(t, execB.Env)

	// Premise: the sentinel from cw.extraEnv is present in the assembled spawn
	// env, so the git-email assertion below reads the same env the spawn sees.
	assert.Equal(t, "present", mA["CTXLOOM_TEST_SENTINEL"], "premise: ExecSpec.Env is built from cw.extraEnv")

	// All four vars reach the exec spawn env.
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		_, ok := mA[k]
		assert.Truef(t, ok, "%s must reach the container spawn env", k)
	}

	// Different agents → different author email in the actual spawn env.
	assert.Equal(t, "agent-a@agents.ctxloom.local", mA["GIT_AUTHOR_EMAIL"])
	assert.Equal(t, "agent-b@agents.ctxloom.local", mB["GIT_AUTHOR_EMAIL"])
	assert.NotEqual(t, mA["GIT_AUTHOR_EMAIL"], mB["GIT_AUTHOR_EMAIL"],
		"prepared for different agentIDs, the container spawn env carries different git identities")
}
