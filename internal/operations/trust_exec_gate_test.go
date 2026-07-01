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

// hookHashOf is the executable-surface hash a bundle-hook grant binds to.
func hookHashOf(h bundles.BundleHook) string { return h.ComputeContentHash() }

const (
	gatePostgresRef = acmeBundle + "tooling#mcp/postgres"
	gateHookRef     = acmeBundle + "tooling#hooks/pre_tool/0"
)

// execGateSeed seeds one REMOTE bundle (acme tooling) with an MCP server and a
// pre_tool hook, keyed by its canonical ref so the gate ref / baseline / grant
// key shapes all agree.
func execGateSeed() map[string]*bundles.Bundle {
	seed := map[string]*bundles.Bundle{
		acmeBundle + "tooling": {
			Name: acmeBundle + "tooling",
			MCP:  map[string]bundles.BundleMCP{"postgres": {Command: "pg-mcp", Args: []string{"--port", "5432"}}},
			Hooks: bundles.BundleHooks{
				PreTool: []bundles.BundleHook{{Matcher: "Bash", Command: "echo hook", Type: "command"}},
			},
		},
	}
	return seed
}

func postgresHash() string {
	return mcpHashOf(bundles.BundleMCP{Command: "pg-mcp", Args: []string{"--port", "5432"}})
}
func toolingHookHash() string {
	return hookHashOf(bundles.BundleHook{Matcher: "Bash", Command: "echo hook", Type: "command"})
}

// TestExecGate_MCP_CascadeResolves drives the REAL cascade through the gate func
// on an MCP-server ref: an explicit grant ALLOWS exposure, no grant from an
// untrusted remote DENIES, and a sticky blacklist DENIES regardless. This is the
// executable choke's deny path — the security-critical surface.
func TestExecGate_MCP_CascadeResolves(t *testing.T) {
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})
	cfg := &config.Config{AppPaths: []string{testBaseDir}}

	// No grant + untrusted remote → DENY.
	store := newTrustStore(t)
	gate := (&contentGate{cfg: cfg, store: store, registry: reg}).allow
	assert.False(t, gate(gatePostgresRef, postgresHash(), "raw"),
		"an ungranted MCP server from an untrusted remote must be withheld")

	// Explicit grant for the exact hash → ALLOW.
	require.NoError(t, store.AddGrant(trustRepo, "tooling#mcp/postgres", postgresHash(), "raw", ""))
	assert.True(t, gate(gatePostgresRef, postgresHash(), "raw"),
		"a granted MCP server must be exposed")

	// A content change (different hash) drops the grant → DENY.
	assert.False(t, gate(gatePostgresRef, "sha256:deadbeef", "raw"),
		"a changed MCP executable surface (new hash) must re-gate")

	// Sticky blacklist beats the grant → DENY.
	require.NoError(t, store.Blacklist(trustRepo, "tooling#mcp/postgres", postgresHash()))
	assert.False(t, gate(gatePostgresRef, postgresHash(), "raw"),
		"a blacklisted MCP server must be withheld even with a matching grant")
}

// TestExecGate_Hook_CascadeResolves drives the REAL cascade on a bundle-hook ref
// ("<bundle>#hooks/<event>/<index>"): grant → ALLOW, no grant (untrusted remote)
// → DENY, blacklist → DENY.
func TestExecGate_Hook_CascadeResolves(t *testing.T) {
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})
	cfg := &config.Config{AppPaths: []string{testBaseDir}}

	store := newTrustStore(t)
	gate := (&contentGate{cfg: cfg, store: store, registry: reg}).allow
	assert.False(t, gate(gateHookRef, toolingHookHash(), "raw"),
		"an ungranted bundle hook from an untrusted remote must be withheld")

	require.NoError(t, store.AddGrant(trustRepo, "tooling#hooks/pre_tool/0", toolingHookHash(), "raw", ""))
	assert.True(t, gate(gateHookRef, toolingHookHash(), "raw"),
		"a granted bundle hook must be applied")

	require.NoError(t, store.Blacklist(trustRepo, "tooling#hooks/pre_tool/0", toolingHookHash()))
	assert.False(t, gate(gateHookRef, toolingHookHash(), "raw"),
		"a blacklisted bundle hook must be withheld")
}

// TestExecGate_BaselineGrantsHooksAndMCP proves the TR6 baseline (extended in
// this change) snapshots pre-existing REMOTE bundle hooks AND MCP servers so they
// survive rollout, while local executables are never granted; and the real gate
// then ALLOWS the baselined versions.
func TestExecGate_BaselineGrantsHooksAndMCP(t *testing.T) {
	seed := execGateSeed()
	// Add a LOCAL bundle whose hook/mcp must NOT be granted.
	seed["demo"] = &bundles.Bundle{
		Name:  "demo",
		MCP:   map[string]bundles.BundleMCP{"localmcp": {Command: "local-cmd"}},
		Hooks: bundles.BundleHooks{PreTool: []bundles.BundleHook{{Command: "echo local", Type: "command"}}},
	}
	loader := bundles.NewLoader(nil, true, bundles.WithSeededBundles(seed))
	store := newTrustStore(t)

	res, err := BaselineTrust(nil, BaselineTrustRequest{Store: store, Loader: loader})
	require.NoError(t, err)
	// remote: mcp + hook granted (2); local: mcp + hook skipped (2).
	assert.Equal(t, 2, res.Granted, "remote MCP + hook baselined")
	assert.Equal(t, 2, res.Local, "local executables tallied but not granted")

	_, mcpOK := store.GrantMatch(trustRepo, "tooling#mcp/postgres", postgresHash())
	assert.True(t, mcpOK, "pre-existing remote MCP server must be baselined")
	_, hookOK := store.GrantMatch(trustRepo, "tooling#hooks/pre_tool/0", toolingHookHash())
	assert.True(t, hookOK, "pre-existing remote bundle hook must be baselined")

	// Local executables are never auto-trusted.
	localMCPHash := mcpHashOf(bundles.BundleMCP{Command: "local-cmd"})
	_, localMCPOK := store.GrantMatch(remote.LocalSource, "demo#mcp/localmcp", localMCPHash)
	assert.False(t, localMCPOK, "local MCP executable must not be baselined")
	_, localHookOK := store.GrantMatch(remote.LocalSource, "demo#hooks/pre_tool/0", hookHashOf(bundles.BundleHook{Command: "echo local", Type: "command"}))
	assert.False(t, localHookOK, "local hook executable must not be baselined")

	// The gate now ALLOWS the baselined remote versions even from an untrusted remote.
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})
	gate := (&contentGate{cfg: &config.Config{AppPaths: []string{testBaseDir}}, store: store, registry: reg}).allow
	assert.True(t, gate(gatePostgresRef, postgresHash(), "raw"), "baselined MCP server survives the gate")
	assert.True(t, gate(gateHookRef, toolingHookHash(), "raw"), "baselined bundle hook survives the gate")
}

// TestExecGate_ResolveBundleMCPServers_RealCascade drives the FULL profile→
// bundle→settings path with the REAL executable gate over a LOCAL on-disk bundle:
// a granted local MCP server is written while an ungranted sibling is withheld,
// and the in-binary builtin servers are exempt.
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

	// Local bundle MCP servers are project-authored, so they auto-trust. A
	// blacklist still withholds one — the denylist/blacklist tiers precede the
	// local tier.
	store, err := getTrustStore(cfg, nil, nil)
	require.NoError(t, err)
	noisyHash := mcpHashOf(bundles.BundleMCP{Command: "npx", Args: []string{"-y", "noisy"}})
	require.NoError(t, store.Blacklist(remote.LocalSource, "mcp-bundle#mcp/noisy-server", noisyHash))

	cfg.SetExecutableTrustGate(NewExecutableTrustGate(cfg).Gate())
	result := cfg.ResolveBundleMCPServers(nil)

	assert.Contains(t, result, "quiet-server", "auto-trusted local MCP server must be written to settings")
	assert.NotContains(t, result, "noisy-server", "blacklisted local MCP executable must be withheld")
	builtin := false
	for _, s := range result {
		if s.SCM == "bundle:builtin:taskloom" {
			builtin = true
		}
	}
	assert.True(t, builtin, "in-binary builtin MCP servers are exempt from the gate")
}

// TestExecGate_ResolveBundleHooks_RealCascade is the hook twin: an auto-trusted
// local bundle hook is applied while a blacklisted sibling is withheld.
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

	// Local bundle hooks auto-trust (project-authored); a blacklist still withholds
	// one — blacklist precedes the local tier.
	store, err := getTrustStore(cfg, nil, nil)
	require.NoError(t, err)
	denyHash := hookHashOf(bundles.BundleHook{Command: "echo deny", Type: "command"})
	require.NoError(t, store.Blacklist(remote.LocalSource, "hook-bundle#hooks/session_start/0", denyHash))

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
	assert.True(t, keepApplied, "auto-trusted local bundle hook must be applied")
	assert.False(t, denyApplied, "blacklisted local bundle hook must NOT be applied")
}

// TestExecGate_FailClosed proves the executable gate withholds on any failure to
// positively justify exposure (denyAll store, unparseable ref) and tallies the
// withheld refs for the "N withheld" advisory.
func TestExecGate_FailClosed(t *testing.T) {
	// denyAll (unreadable store) → every executable withheld, all recorded.
	g := &contentGate{denyAll: true}
	e := &ExecutableTrustGate{gate: g}
	assert.False(t, e.Gate()(gatePostgresRef, postgresHash(), "raw"))
	assert.False(t, e.Gate()(gateHookRef, toolingHookHash(), "raw"))
	assert.Len(t, g.withheldRefs(), 2, "fail-closed: both executables recorded as withheld")

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

// TestExecGate_CLIHookTrustThenBlacklist ties the interactive/CLI hook-trust
// mutations to the TR5 exec choke: a hook the user trusts via SetItemTrust (the
// `ctxloom trust <bundle>#hooks/<id>` / `bundle show -i` [t] path) then passes
// the executable gate, and a hook the user blacklists via SetBlacklist (the [b]
// path) is withheld by that same gate — the sticky ref block beats the grant.
// The gate is rebuilt after each mutation so it reads the freshly-persisted store.
func TestExecGate_CLIHookTrustThenBlacklist(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	bundlesDir := filepath.Join(appDir, "cache", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hookb.yaml"),
		[]byte("name: hookb\nversion: \"1.0\"\nhooks:\n  pre_tool:\n    - matcher: Bash\n      command: echo keep\n      type: command\n"), 0o644))

	cfg := &config.Config{AppPaths: []string{appDir}}
	ref := "hookb#hooks/pre_tool/0"
	hookHash := hookHashOf(bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"})

	// A project-local bundle hook auto-trusts (no grant needed) → passes the gate.
	assert.True(t, NewExecutableTrustGate(cfg).Gate()(ref, hookHash, "raw"),
		"an auto-trusted local bundle hook must pass the exec gate")

	// CLI blacklist → the exec gate withholds it (sticky block beats local auto-trust).
	_, err := SetBlacklist(cfg, SetBlacklistRequest{Ref: ref})
	require.NoError(t, err)
	assert.False(t, NewExecutableTrustGate(cfg).Gate()(ref, hookHash, "raw"),
		"a CLI-blacklisted bundle hook must be withheld by the exec gate")
}

// Ensure trust.KindHook participates in the dir/selector grammar end to end.
func TestKindHook_Dir(t *testing.T) {
	assert.Equal(t, "hooks", trust.KindHook.Dir())
	assert.False(t, trust.KindHook.IsContent(), "a hook is an executable surface, not auto-allowed content")
}
