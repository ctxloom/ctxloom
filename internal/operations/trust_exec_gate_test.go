package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// hookHashOf is the executable-surface hash a bundle-hook acceptance binds to.
func hookHashOf(h bundles.BundleHook) string { return h.ComputeContentHash() }

const (
	gatePostgresRef = acmeBundle + "tooling#mcp/postgres"
	gateHookRef     = acmeBundle + "tooling#hooks/pre_tool/0"
)

func postgresHash() string {
	return mcpHashOf(bundles.BundleMCP{Command: "pg-mcp", Args: []string{"--port", "5432"}})
}
func toolingHookHash() string {
	return hookHashOf(bundles.BundleHook{Matcher: "Bash", Command: "echo hook", Type: "command"})
}

// TestExecGate_MCP_CascadeResolves drives the REAL decision function through
// the gate func on an MCP-server ref: an acceptance ALLOWS exposure, no
// acceptance from an untrusted remote DENIES (pending), and a rejection DENIES
// regardless. This is the executable choke's deny path — the security-critical
// surface.
func TestExecGate_MCP_CascadeResolves(t *testing.T) {
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})
	cfg := &config.Config{AppPaths: []string{testBaseDir}}

	// Pending (never reviewed) + untrusted remote → DENY.
	store := newTrustStore(t)
	gate := (&contentGate{cfg: cfg, store: store, registry: reg}).allow
	assert.False(t, gate(gatePostgresRef, postgresHash(), "raw"),
		"an unreviewed MCP server from an untrusted remote must be withheld")

	// Acceptance at the exact hash → ALLOW.
	require.NoError(t, store.SetAccepted(trustRepo, "tooling#mcp/postgres", postgresHash(), ""))
	assert.True(t, gate(gatePostgresRef, postgresHash(), "raw"),
		"an accepted MCP server must be exposed")

	// A content change (different hash) returns it to pending → DENY.
	assert.False(t, gate(gatePostgresRef, "sha256:deadbeef", "raw"),
		"a changed MCP executable surface (new hash) must re-gate")

	// Rejection beats the acceptance → DENY.
	require.NoError(t, store.SetRejected(trustRepo, "tooling#mcp/postgres", postgresHash()))
	assert.False(t, gate(gatePostgresRef, postgresHash(), "raw"),
		"a rejected MCP server must be withheld even after a prior acceptance")
}

// TestExecGate_Hook_CascadeResolves drives the REAL decision function on a
// bundle-hook ref ("<bundle>#hooks/<event>/<index>"): accepted → ALLOW,
// pending (untrusted remote) → DENY, rejected → DENY.
func TestExecGate_Hook_CascadeResolves(t *testing.T) {
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})
	cfg := &config.Config{AppPaths: []string{testBaseDir}}

	store := newTrustStore(t)
	gate := (&contentGate{cfg: cfg, store: store, registry: reg}).allow
	assert.False(t, gate(gateHookRef, toolingHookHash(), "raw"),
		"an unreviewed bundle hook from an untrusted remote must be withheld")

	require.NoError(t, store.SetAccepted(trustRepo, "tooling#hooks/pre_tool/0", toolingHookHash(), ""))
	assert.True(t, gate(gateHookRef, toolingHookHash(), "raw"),
		"an accepted bundle hook must be applied")

	require.NoError(t, store.SetRejected(trustRepo, "tooling#hooks/pre_tool/0", toolingHookHash()))
	assert.False(t, gate(gateHookRef, toolingHookHash(), "raw"),
		"a rejected bundle hook must be withheld")
}

// TestExecGate_TrustedSourceExemptsExecutables proves the first-party
// trusted-source exemption covers cloned executables (the ctxloom-default
// case): with the remote in the trusted-sources set, its MCP server and hook
// pass the exec gate with no per-item review state at all — while a rejection
// still beats the exemption.
func TestExecGate_TrustedSourceExemptsExecutables(t *testing.T) {
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: true})
	cfg := &config.Config{AppPaths: []string{testBaseDir}}
	store := newTrustStore(t)
	gate := (&contentGate{cfg: cfg, store: store, registry: reg}).allow

	assert.True(t, gate(gatePostgresRef, postgresHash(), "raw"),
		"a trusted source's MCP server is exempt from review")
	assert.True(t, gate(gateHookRef, toolingHookHash(), "raw"),
		"a trusted source's bundle hook is exempt from review")

	require.NoError(t, store.SetRejected(trustRepo, "tooling#mcp/postgres", postgresHash()))
	assert.False(t, gate(gatePostgresRef, postgresHash(), "raw"),
		"rejection beats the trusted-source exemption")
}

// TestExecGate_ResolveBundleMCPServers_RealCascade drives the FULL profile→
// bundle→settings path with the REAL executable gate over a LOCAL on-disk bundle:
// a first-party local MCP server is written while a rejected sibling is
// withheld, and the in-binary builtin servers are exempt.
func TestExecGate_ResolveBundleMCPServers_RealCascade(t *testing.T) {
	restore := config.SetLookPathForTesting(func(string) (string, error) { return "/usr/bin/x", nil })
	defer restore()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := filepath.Join(appDir, "cache", "bundles")
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte("name: dev\nbundles:\n  - mcp-bundle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "mcp-bundle.yaml"),
		[]byte("name: mcp-bundle\nversion: \"1.0\"\nmcp:\n  quiet-server:\n    command: npx\n    args: [\"-y\", \"quiet\"]\n  noisy-server:\n    command: npx\n    args: [\"-y\", \"noisy\"]\n"), 0o644))

	cfg := &config.Config{Profiles: config.ProfilesConfig{Defaults: []string{"dev"}}, AppPaths: []string{appDir}}

	// Local bundle MCP servers are project-authored (first-party), so they are
	// exposed with no review state. A rejection still withholds one — the
	// rejected step precedes the local exemption.
	store, err := getTrustStore(cfg, nil, nil)
	require.NoError(t, err)
	noisyHash := mcpHashOf(bundles.BundleMCP{Command: "npx", Args: []string{"-y", "noisy"}})
	require.NoError(t, store.SetRejected(remote.LocalSource, "mcp-bundle#mcp/noisy-server", noisyHash))

	cfg.SetExecutableTrustGate(NewExecutableTrustGate(cfg).Gate())
	result := cfg.ResolveBundleMCPServers(nil)

	assert.Contains(t, result, "quiet-server", "first-party local MCP server must be written to settings")
	assert.NotContains(t, result, "noisy-server", "rejected local MCP executable must be withheld")
	builtin := false
	for _, s := range result {
		if s.SCM == "bundle:builtin:taskloom" {
			builtin = true
		}
	}
	assert.True(t, builtin, "in-binary builtin MCP servers are exempt from the gate")
}

// TestExecGate_ResolveBundleHooks_RealCascade is the hook twin: a first-party
// local bundle hook is applied while a rejected sibling is withheld.
func TestExecGate_ResolveBundleHooks_RealCascade(t *testing.T) {
	restore := config.SetLookPathForTesting(func(string) (string, error) { return "/usr/bin/x", nil })
	defer restore()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := filepath.Join(appDir, "cache", "bundles")
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte("name: dev\nbundles:\n  - hook-bundle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hook-bundle.yaml"),
		[]byte("name: hook-bundle\nversion: \"1.0\"\nhooks:\n  pre_tool:\n    - matcher: Bash\n      command: echo keep\n      type: command\n  session_start:\n    - command: echo deny\n      type: command\n"), 0o644))

	cfg := &config.Config{Profiles: config.ProfilesConfig{Defaults: []string{"dev"}}, AppPaths: []string{appDir}}

	// Local bundle hooks are first-party (project-authored); a rejection still
	// withholds one — the rejected step precedes the local exemption.
	store, err := getTrustStore(cfg, nil, nil)
	require.NoError(t, err)
	denyHash := hookHashOf(bundles.BundleHook{Command: "echo deny", Type: "command"})
	require.NoError(t, store.SetRejected(remote.LocalSource, "hook-bundle#hooks/session_start/0", denyHash))

	cfg.SetExecutableTrustGate(NewExecutableTrustGate(cfg).Gate())
	result := cfg.ResolveBundleHooks(nil)

	keepApplied, denyApplied := false, false
	for _, h := range result.PreTool {
		if h.Command == "echo keep" && h.SCM == "bundle:hook-bundle" {
			keepApplied = true
		}
	}
	for _, h := range result.SessionStart {
		if h.Command == "echo deny" && h.SCM == "bundle:hook-bundle" {
			denyApplied = true
		}
	}
	assert.True(t, keepApplied, "first-party local bundle hook must be applied")
	assert.False(t, denyApplied, "rejected local bundle hook must NOT be applied")
}

// TestExecGate_FailClosed proves the executable gate withholds on any failure to
// positively justify exposure (denyAll store, unparseable ref) and tallies the
// withheld refs for the pending advisory.
func TestExecGate_FailClosed(t *testing.T) {
	// denyAll (unreadable store) → every executable withheld, all recorded.
	g := &contentGate{denyAll: true}
	e := &ExecutableTrustGate{gate: g}
	assert.False(t, e.Gate()(gatePostgresRef, postgresHash(), "raw"))
	assert.False(t, e.Gate()(gateHookRef, toolingHookHash(), "raw"))
	assert.Len(t, g.withheldRefs(), 2, "fail-closed: both executables recorded as withheld")
	pending, rejected := g.withheldTally()
	assert.Equal(t, 2, pending, "fail-closed withholds tally as pending")
	assert.Equal(t, 0, rejected)

	// Unparseable ref → withhold (no selector).
	g2 := &contentGate{cfg: &config.Config{AppPaths: []string{testBaseDir}}, store: newTrustStore(t)}
	assert.False(t, g2.allow("garbage-without-selector", "sha256:abc", "raw"),
		"a ref the gate cannot address must be withheld")
	assert.Contains(t, g2.withheldRefs(), "garbage-without-selector")

	// A nil *ExecutableTrustGate is a no-op (no gating, no panic).
	var nilGate *ExecutableTrustGate
	assert.Nil(t, nilGate.Gate())
	nilGate.WarnWithheld()
}

// TestExecGate_WithheldTallySplitsRejected proves the advisory tally separates
// pending from rejected: one unreviewed item and one rejected item withhold as
// (1 pending, 1 rejected).
func TestExecGate_WithheldTallySplitsRejected(t *testing.T) {
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})
	store := newTrustStore(t)
	require.NoError(t, store.SetRejected(trustRepo, "tooling#hooks/pre_tool/0", toolingHookHash()))
	g := &contentGate{cfg: &config.Config{AppPaths: []string{testBaseDir}}, store: store, registry: reg}

	assert.False(t, g.allow(gatePostgresRef, postgresHash(), "raw"), "pending item withheld")
	assert.False(t, g.allow(gateHookRef, toolingHookHash(), "raw"), "rejected item withheld")
	pending, rejected := g.withheldTally()
	assert.Equal(t, 1, pending)
	assert.Equal(t, 1, rejected)
}

// TestExecGate_CLIHookTrustThenBlacklist ties the interactive/CLI hook-trust
// mutations to the exec choke: a project-local hook is first-party (passes the
// gate with no review state), and a hook the user rejects via SetBlacklist (the
// `ctxloom blacklist` / [b] path) is withheld by that same gate — the rejection
// beats the local exemption. The gate is rebuilt after the mutation so it reads
// the freshly-persisted store.
func TestExecGate_CLIHookTrustThenBlacklist(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	bundlesDir := filepath.Join(appDir, "cache", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hookb.yaml"),
		[]byte("name: hookb\nversion: \"1.0\"\nhooks:\n  pre_tool:\n    - matcher: Bash\n      command: echo keep\n      type: command\n"), 0o644))

	cfg := &config.Config{AppPaths: []string{appDir}}
	ref := "hookb#hooks/pre_tool/0"
	hookHash := hookHashOf(bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"})

	// A project-local bundle hook is first-party (no acceptance needed) → passes.
	assert.True(t, NewExecutableTrustGate(cfg).Gate()(ref, hookHash, "raw"),
		"a first-party local bundle hook must pass the exec gate")

	// CLI rejection → the exec gate withholds it (rejection beats the local
	// exemption).
	_, err := SetBlacklist(cfg, SetBlacklistRequest{Ref: ref})
	require.NoError(t, err)
	assert.False(t, NewExecutableTrustGate(cfg).Gate()(ref, hookHash, "raw"),
		"a CLI-rejected bundle hook must be withheld by the exec gate")
}

// Ensure trust.KindHook participates in the dir/selector grammar end to end.
func TestKindHook_Dir(t *testing.T) {
	assert.Equal(t, "hooks", trust.KindHook.Dir())
	assert.False(t, trust.KindHook.IsContent(), "a hook is an executable surface, not content")
}
