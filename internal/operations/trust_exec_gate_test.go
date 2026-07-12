package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
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

func postgresPayload() []byte {
	return mcpPayloadOf(bundles.BundleMCP{Command: "pg-mcp", Args: []string{"--port", "5432"}})
}
func toolingHookPayload() []byte {
	p, err := (&bundles.BundleHook{Matcher: "Bash", Command: "echo hook", Type: "command"}).ContentPayload()
	if err != nil {
		panic(err)
	}
	return p
}
func postgresHash() string    { return bundles.HashPayload(postgresPayload()) }
func toolingHookHash() string { return bundles.HashPayload(toolingHookPayload()) }

// TestExecGate_MCP_CascadeResolves drives the REAL decision function through
// the gate func on an MCP-server ref: an approval ALLOWS exposure, an unsigned
// unreviewed item DENIES (pending), and a rejection DENIES regardless. This is
// the executable choke's deny path — the security-critical surface.
func TestExecGate_MCP_CascadeResolves(t *testing.T) {
	cfg := &config.Config{AppPaths: []string{testBaseDir}}

	// Pending (never reviewed) + unsigned → DENY.
	store := newTrustStore(t)
	gate := (&contentGate{cfg: cfg, store: store}).allow
	assert.False(t, gate(gatePostgresRef, postgresPayload(), "raw", ""),
		"an unreviewed unsigned MCP server must be withheld")

	// Approval of the exact bytes → ALLOW.
	require.NoError(t, store.SetAccepted(trustRepo, "tooling#mcp/postgres", postgresHash(), ""))
	assert.True(t, gate(gatePostgresRef, postgresPayload(), "raw", ""),
		"an approved MCP server must be exposed")

	// A content change (different bytes) returns it to pending → DENY.
	assert.False(t, gate(gatePostgresRef, pbytes("changed"), "raw", ""),
		"a changed MCP executable surface (new bytes) must re-gate")

	// Rejection beats the approval → DENY.
	require.NoError(t, store.SetRejected(trustRepo, "tooling#mcp/postgres", postgresHash()))
	assert.False(t, gate(gatePostgresRef, postgresPayload(), "raw", ""),
		"a rejected MCP server must be withheld even after a prior approval")
}

// TestExecGate_Hook_CascadeResolves drives the REAL decision function on a
// bundle-hook ref: approved → ALLOW, unsigned unreviewed → DENY, rejected → DENY.
func TestExecGate_Hook_CascadeResolves(t *testing.T) {
	cfg := &config.Config{AppPaths: []string{testBaseDir}}

	store := newTrustStore(t)
	gate := (&contentGate{cfg: cfg, store: store}).allow
	assert.False(t, gate(gateHookRef, toolingHookPayload(), "raw", ""),
		"an unreviewed unsigned bundle hook must be withheld")

	require.NoError(t, store.SetAccepted(trustRepo, "tooling#hooks/pre_tool/0", toolingHookHash(), ""))
	assert.True(t, gate(gateHookRef, toolingHookPayload(), "raw", ""),
		"an approved bundle hook must be applied")

	require.NoError(t, store.SetRejected(trustRepo, "tooling#hooks/pre_tool/0", toolingHookHash()))
	assert.False(t, gate(gateHookRef, toolingHookPayload(), "raw", ""),
		"a rejected bundle hook must be withheld")
}

// TestExecGate_TrustedSignerExemptsExecutables proves step 4 covers signed
// executables (the ctxloom-default / org case): a bundle carrying a verified
// publisher signer passes the exec gate with no per-item review state at all —
// while a rejection still beats the exemption.
func TestExecGate_TrustedSignerExemptsExecutables(t *testing.T) {
	cfg := &config.Config{AppPaths: []string{testBaseDir}}
	store := newTrustStore(t)
	gate := (&contentGate{cfg: cfg, store: store}).allow

	assert.True(t, gate(gatePostgresRef, postgresPayload(), "raw", trustedPublisher),
		"a trusted publisher's MCP server is exempt from review")
	assert.True(t, gate(gateHookRef, toolingHookPayload(), "raw", trustedPublisher),
		"a trusted publisher's bundle hook is exempt from review")

	require.NoError(t, store.SetRejected(trustRepo, "tooling#mcp/postgres", postgresHash()))
	assert.False(t, gate(gatePostgresRef, postgresPayload(), "raw", trustedPublisher),
		"rejection beats the trusted-signer exemption")
}

// TestExecGate_ResolveBundleMCPServers_RealCascade drives the FULL profile→
// bundle→settings path with the REAL executable gate over a LOCAL on-disk bundle:
// a first-party local MCP server is written while a rejected sibling is
// withheld, and the in-binary builtin servers — routed through the same REAL
// decision function, allowed by default at their own step — still come
// through with no review state recorded (see
// TestExecGate_ResolveBundleMCPServers_BuiltinRejectable for the companion
// proof that a REJECTED builtin item is withheld).
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

	cfg := &config.Config{DefaultAgent: "default", Agents: map[string]agents.Agent{"default": {Profiles: []string{"dev"}}}, AppPaths: []string{appDir}}

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
	assert.True(t, builtin, "in-binary builtin MCP servers are allowed by default (not rejected) through the same gate")
}

// TestExecGate_ResolveBundleMCPServers_BuiltinRejectable proves builtin bundles
// are reachable by the rejection step — docs/trust-model.md states "a user can
// reject an item even from a trusted source or a builtin" (rejection beats
// everything), but until this fix builtin resolvers passed gate=nil and could
// never be rejected. A REJECTED builtin MCP server (the taskloom companion's
// "taskloom" server) must be withheld exactly like a rejected remote/local one.
func TestExecGate_ResolveBundleMCPServers_BuiltinRejectable(t *testing.T) {
	restore := config.SetLookPathForTesting(func(string) (string, error) { return "/usr/bin/x", nil })
	defer restore()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	cfg := &config.Config{AppPaths: []string{appDir}}

	store, err := getTrustStore(cfg, nil, nil)
	require.NoError(t, err)
	// Matches resources/builtin_bundles/taskloom.yaml's mcp.taskloom entry
	// exactly (Command/Args/Installation are the ComputeContentHash preimage —
	// Notes is excluded).
	taskloomHash := mcpHashOf(bundles.BundleMCP{
		Command:      "taskloom",
		Args:         []string{"mcp"},
		Installation: "brew install ctxloom/tap/taskloom",
	})
	require.NoError(t, store.SetRejected("builtin:ctxloom", "taskloom#mcp/taskloom", taskloomHash))

	cfg.SetExecutableTrustGate(NewExecutableTrustGate(cfg).Gate())
	result := cfg.ResolveBundleMCPServers(nil)

	assert.NotContains(t, result, "taskloom",
		"a REJECTED builtin MCP server must be withheld — rejection beats the builtin exemption")
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

	cfg := &config.Config{DefaultAgent: "default", Agents: map[string]agents.Agent{"default": {Profiles: []string{"dev"}}}, AppPaths: []string{appDir}}

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
	assert.False(t, e.Gate()(gatePostgresRef, postgresPayload(), "raw", ""))
	assert.False(t, e.Gate()(gateHookRef, toolingHookPayload(), "raw", ""))
	assert.Len(t, g.withheldRefs(), 2, "fail-closed: both executables recorded as withheld")
	pending, rejected := g.withheldTally()
	assert.Equal(t, 2, pending, "fail-closed withholds tally as pending")
	assert.Equal(t, 0, rejected)

	// Unparseable ref → withhold (no selector).
	g2 := &contentGate{cfg: &config.Config{AppPaths: []string{testBaseDir}}, store: newTrustStore(t)}
	assert.False(t, g2.allow("garbage-without-selector", pbytes("abc"), "raw", ""),
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
	store := newTrustStore(t)
	require.NoError(t, store.SetRejected(trustRepo, "tooling#hooks/pre_tool/0", toolingHookHash()))
	g := &contentGate{cfg: &config.Config{AppPaths: []string{testBaseDir}}, store: store}

	assert.False(t, g.allow(gatePostgresRef, postgresPayload(), "raw", ""), "pending item withheld")
	assert.False(t, g.allow(gateHookRef, toolingHookPayload(), "raw", ""), "rejected item withheld")
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
	hookPayload, err := (&bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}).ContentPayload()
	require.NoError(t, err)

	// A project-local bundle hook is first-party (no acceptance needed) → passes.
	assert.True(t, NewExecutableTrustGate(cfg).Gate()(ref, hookPayload, "raw", ""),
		"a first-party local bundle hook must pass the exec gate")

	// CLI rejection → the exec gate withholds it (rejection beats the local
	// exemption).
	_, err = SetBlacklist(cfg, SetBlacklistRequest{Ref: ref})
	require.NoError(t, err)
	assert.False(t, NewExecutableTrustGate(cfg).Gate()(ref, hookPayload, "raw", ""),
		"a CLI-rejected bundle hook must be withheld by the exec gate")
}

// Ensure trust.KindHook participates in the dir/selector grammar end to end.
func TestKindHook_Dir(t *testing.T) {
	assert.Equal(t, "hooks", trust.KindHook.Dir())
	assert.False(t, trust.KindHook.IsContent(), "a hook is an executable surface, not content")
}
