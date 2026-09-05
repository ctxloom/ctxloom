package operations

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// gatePostgresRef / gateHookRef are canonical bundle-reference grammar item
// refs — the shape bundles.Decide itself now parses (trust.ParseBundleRef),
// not acmeBundle's "<url>@bundles/<name>" concatenation these tests feed
// Decide directly with (rather than through a producer). They round-trip
// through trust.RefFromBundleRef to the IDENTICAL trust.Ref{RepoURL:
// trustRepo, Bundle: "tooling", ...} these tests reject/retract by hand
// elsewhere in this file, which is what keeps a rejection written under one
// spelling reachable by a Decide call built from the other.
var (
	gatePostgresRef = mustGitItemRef("github.com", "/acme/repo", "tooling", trust.KindMCP, "postgres")
	gateHookRef     = mustGitItemRef("github.com", "/acme/repo", "tooling", trust.KindHook, "pre_tool/0")
)

// mustGitItemRef mints a canonical git-class item ref for a "<host><repoPath>"
// test fixture repository — every hand-built (not producer-derived) fixture
// ref this package feeds directly to bundles.Decide/admitFragment/admitExec
// goes through this one mint, so the grammar cannot drift between fixtures.
func mustGitItemRef(host, repoPath, bundle string, kind trust.ItemKind, item string) string {
	br, err := trust.GitRef(host, repoPath, bundle)
	if err != nil {
		panic(err)
	}
	full, err := br.WithItem(kind, item)
	if err != nil {
		panic(err)
	}
	return full.String()
}

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

// TestExecGate_MCP_CascadeResolves drives the REAL decision function through
// the gate func on an MCP-server ref: an approval ALLOWS exposure, an unsigned
// unreviewed item DENIES (pending), and a rejection DENIES regardless. This is
// the executable choke's deny path — the security-critical surface.
func TestExecGate_MCP_CascadeResolves(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)

	// Pending (never reviewed) + unsigned → DENY.
	gate := &contentGate{cfg: cfg, records: fx.records()}
	unsigned := execRead(t, "")
	assert.False(t, admitExec(t, gate, unsigned, gatePostgresRef, postgresPayload(), "raw"),
		"an unreviewed unsigned MCP server must be withheld")

	// Approval of the exact bytes → ALLOW.
	postgresRef := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"}
	fx.approve(postgresRef, signing.FormRaw, postgresPayload())
	assert.True(t, admitExec(t, gate, unsigned, gatePostgresRef, postgresPayload(), "raw"),
		"an approved MCP server must be exposed")

	// A content change (different bytes) returns it to pending → DENY.
	assert.False(t, admitExec(t, gate, unsigned, gatePostgresRef, pbytes("changed"), "raw"),
		"a changed MCP executable surface (new bytes) must re-gate")

	// Rejection beats the approval → DENY.
	fx.rejectItem(postgresRef, signing.FormRaw, postgresPayload())
	assert.False(t, admitExec(t, gate, unsigned, gatePostgresRef, postgresPayload(), "raw"),
		"a rejected MCP server must be withheld even after a prior approval")
}

// TestExecGate_Hook_CascadeResolves drives the REAL decision function on a
// bundle-hook ref: approved → ALLOW, unsigned unreviewed → DENY, rejected → DENY.
func TestExecGate_Hook_CascadeResolves(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)

	gate := &contentGate{cfg: cfg, records: fx.records()}
	unsigned := execRead(t, "")
	assert.False(t, admitExec(t, gate, unsigned, gateHookRef, toolingHookPayload(), "raw"),
		"an unreviewed unsigned bundle hook must be withheld")

	hookRef := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindHook, Name: "pre_tool/0"}
	fx.approve(hookRef, signing.FormRaw, toolingHookPayload())
	assert.True(t, admitExec(t, gate, unsigned, gateHookRef, toolingHookPayload(), "raw"),
		"an approved bundle hook must be applied")

	fx.rejectItem(hookRef, signing.FormRaw, toolingHookPayload())
	assert.False(t, admitExec(t, gate, unsigned, gateHookRef, toolingHookPayload(), "raw"),
		"a rejected bundle hook must be withheld")
}

// TestExecGate_TrustedSignerExemptsExecutables proves step 4 covers signed
// executables (the ctxloom-default / org case): a bundle carrying a verified
// publisher signer passes the exec gate with no per-item review state at all —
// while a rejection still beats the exemption.
func TestExecGate_TrustedSignerExemptsExecutables(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)
	gate := &contentGate{cfg: cfg, records: fx.records()}
	signed := execRead(t, trustedPublisher)

	assert.True(t, admitExec(t, gate, signed, gatePostgresRef, postgresPayload(), "raw"),
		"a trusted publisher's MCP server is exempt from review")
	assert.True(t, admitExec(t, gate, signed, gateHookRef, toolingHookPayload(), "raw"),
		"a trusted publisher's bundle hook is exempt from review")

	postgresRef := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"}
	fx.rejectItem(postgresRef, signing.FormRaw, postgresPayload())
	assert.False(t, admitExec(t, gate, signed, gatePostgresRef, postgresPayload(), "raw"),
		"rejection beats the trusted-signer exemption")
}

// TestExecGate_ResolveBundleMCPServers_RealCascade drives the FULL profile→
// bundle→settings path with the REAL executable gate over a LOCAL on-disk
// bundle: a first-party local MCP server is written while a rejected sibling
// is withheld. See TestExecGate_ResolveBundleMCPServers_CompanionRejectable
// for the companion-loadout analogue of "an item can still be rejected
// beyond its default exemption" — ltk/taskloom moved off the in-binary
// builtin exemption (this test used to assert on) onto their own loadouts,
// which are trusted-signer/pending like any other third-party content, never
// exempt.
func TestExecGate_ResolveBundleMCPServers_RealCascade(t *testing.T) {
	restore := config.SetLookPathForTesting(func(string) (string, error) { return "", exec.ErrNotFound })
	defer restore()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte("name: dev\nbundles:\n  - mcp-bundle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "mcp-bundle.yaml"),
		[]byte("version: \"1.0\"\nmcp:\n  quiet-server:\n    command: npx\n    args: [\"-y\", \"quiet\"]\n  noisy-server:\n    command: npx\n    args: [\"-y\", \"noisy\"]\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{DefaultAgent: "default", Agents: map[string]agents.Agent{"default": {Profiles: []string{"dev"}}}, AppPaths: []string{appDir}})

	// Local bundle MCP servers are project-authored (first-party), so they are
	// exposed with no review state. A rejection still withholds one — the
	// rejected step precedes the local exemption. Recorded UNSIGNED (spec
	// §9.5) directly in the real user store the default gate reads.
	noisyPayload := mcpPayloadOf(bundles.BundleMCP{Command: "npx", Args: []string{"-y", "noisy"}})
	installUnsignedRejection(t,
		trust.Ref{Bundle: "mcp-bundle", Kind: trust.KindMCP, Name: "noisy-server", IsLocal: true},
		signing.FormRaw, noisyPayload)

	cfg.SetExecutableTrustGate(NewExecutableTrustGate(cfg).Authorizer())
	result := cfg.ResolveBundleMCPServers(nil)

	assert.Contains(t, result, "quiet-server", "first-party local MCP server must be written to settings")
	assert.NotContains(t, result, "noisy-server", "rejected local MCP executable must be withheld")
}

// TestExecGate_ResolveBundleMCPServers_CompanionRejectable proves a companion
// loadout's MCP server is reachable by the rejection step exactly like any
// other third-party content: docs/trust-model.md states rejection beats
// everything, including a trusted signer. This is the companion-loadout
// analogue of the deleted TestExecGate_ResolveBundleMCPServers_BuiltinRejectable
// (ltk/taskloom moved off the in-binary builtin exemption that test used
// to drive onto their own loadouts, which are never exempt).
func TestExecGate_ResolveBundleMCPServers_CompanionRejectable(t *testing.T) {
	restoreLook := config.SetLookPathForTesting(func(bin string) (string, error) {
		if bin == "ltk" {
			return "/fake/ltk", nil
		}
		return "", exec.ErrNotFound
	})
	defer restoreLook()
	envelope, err := signing.EncodeLoadoutEnvelope(
		[]byte("version: \"1.0.0\"\nmcp:\n  ltk-server:\n    command: ltk\n    args: [\"serve\"]\n"), nil, "")
	require.NoError(t, err)
	restoreProbe := config.SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	ltkPayload := mcpPayloadOf(bundles.BundleMCP{Command: "ltk", Args: []string{"serve"}})
	installUnsignedRejection(t,
		trust.Ref{RepoURL: remote.CompanionSource, Bundle: "ltk", Kind: trust.KindMCP, Name: "ltk-server"},
		signing.FormRaw, ltkPayload)

	cfg.SetExecutableTrustGate(NewExecutableTrustGate(cfg).Authorizer())
	result := cfg.ResolveBundleMCPServers(nil)

	assert.NotContains(t, result, "ltk-server",
		"a REJECTED companion MCP server must be withheld — rejection beats the trusted-signer exemption, and it was pending (unsigned) besides")
}

// TestExecGate_ResolveBundleHooks_RealCascade is the hook twin: a first-party
// local bundle hook is applied while a rejected sibling is withheld.
func TestExecGate_ResolveBundleHooks_RealCascade(t *testing.T) {
	restore := config.SetLookPathForTesting(func(string) (string, error) { return "/usr/bin/x", nil })
	defer restore()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte("name: dev\nbundles:\n  - hook-bundle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hook-bundle.yaml"),
		[]byte("version: \"1.0\"\nhooks:\n  pre_tool:\n    - matcher: Bash\n      command: echo keep\n      type: command\n  session_start:\n    - command: echo deny\n      type: command\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{DefaultAgent: "default", Agents: map[string]agents.Agent{"default": {Profiles: []string{"dev"}}}, AppPaths: []string{appDir}})

	// Local bundle hooks are first-party (project-authored); a rejection still
	// withholds one — the rejected step precedes the local exemption.
	// Recorded UNSIGNED (spec §9.5) directly in the real user store.
	denyPayload, perr := (&bundles.BundleHook{Command: "echo deny", Type: "command"}).ContentPayload()
	require.NoError(t, perr)
	installUnsignedRejection(t,
		trust.Ref{Bundle: "hook-bundle", Kind: trust.KindHook, Name: "session_start/0", IsLocal: true},
		signing.FormRaw, denyPayload)

	cfg.SetExecutableTrustGate(NewExecutableTrustGate(cfg).Authorizer())
	result := cfg.ResolveBundleHooks(nil)

	keepApplied, denyApplied := false, false
	for _, h := range result.PreTool {
		if h.Command == "echo keep" && h.SCM == "bundle:ctxloom+local:hook-bundle" {
			keepApplied = true
		}
	}
	for _, h := range result.SessionStart {
		if h.Command == "echo deny" && h.SCM == "bundle:ctxloom+local:hook-bundle" {
			denyApplied = true
		}
	}
	assert.True(t, keepApplied, "first-party local bundle hook must be applied")
	assert.False(t, denyApplied, "rejected local bundle hook must NOT be applied")
}

// TestExecGate_FailClosed proves the executable gate withholds on any failure
// to positively justify exposure (a fresh/empty records store — nothing ever
// approved — and an unparseable ref) and tallies the withheld refs for the
// pending advisory.
func TestExecGate_FailClosed(t *testing.T) {
	// A fresh, empty records store → every executable withheld, all recorded
	// as pending (nothing has ever been approved).
	g := &contentGate{records: newTrustFixture(t).records()}
	e := &ExecutableTrustGate{gate: g}
	unsigned := execRead(t, "")
	assert.False(t, bundles.Decide(e.Authorizer(), unsigned, gatePostgresRef, postgresPayload(), bundles.FormRaw).Allow)
	assert.False(t, bundles.Decide(e.Authorizer(), unsigned, gateHookRef, toolingHookPayload(), bundles.FormRaw).Allow)
	assert.Len(t, g.withheldRefs(), 2, "fail-closed: both executables recorded as withheld")
	pending, rejected := withheldStateTally(g)
	assert.Equal(t, 2, pending, "fail-closed withholds tally as pending")
	assert.Equal(t, 0, rejected)

	// Unparseable ref → withhold (no selector).
	g2 := &contentGate{cfg: config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}}), records: newTrustFixture(t).records()}
	assert.False(t, admitExec(t, g2, unsigned, "garbage-without-selector", pbytes("abc"), "raw"),
		"a ref the gate cannot address must be withheld")
	// Recorded under the ref VERBATIM (bundles.UnaddressableReporter): the
	// string the caller used is the only identity such an item has, and the
	// executable surfaces read this tally to build their advisory.
	assert.Contains(t, g2.withheldRefs(), "garbage-without-selector")

	// An UNCLAIMED read — one no reader produced — withholds too, and is
	// recorded, because it IS addressable.
	assert.False(t, admitExec(t, g2, bundles.BundleRead{}, gatePostgresRef, postgresPayload(), "raw"),
		"a read that established nothing must never be treated as local/unsigned")
	assert.Contains(t, g2.withheldRefs(), gatePostgresRef)

	// A nil *ExecutableTrustGate is a no-op (no gating, no panic), and it says so
	// with AdmitAll rather than handing back a nil that would withhold
	// everything downstream.
	var nilGate *ExecutableTrustGate
	assert.False(t, bundles.Gates(nilGate.Authorizer()),
		"a nil gate must hand back the ungated authorizer, not a real one")
	assert.True(t, nilGate.Authorizer().Admit(bundles.Exposure{}).Allow,
		"the no-op gate must admit")
	nilGate.WarnWithheld()
}

// TestExecGate_WithheldTallySplitsRejected proves the advisory tally separates
// pending from rejected: one unreviewed item and one rejected item withhold as
// (1 pending, 1 rejected).
func TestExecGate_WithheldTallySplitsRejected(t *testing.T) {
	fx := newTrustFixture(t)
	fx.rejectItem(trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindHook, Name: "pre_tool/0"}, signing.FormRaw, toolingHookPayload())
	g := &contentGate{cfg: config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}}), records: fx.records()}

	unsigned := execRead(t, "")
	assert.False(t, admitExec(t, g, unsigned, gatePostgresRef, postgresPayload(), "raw"), "pending item withheld")
	assert.False(t, admitExec(t, g, unsigned, gateHookRef, toolingHookPayload(), "raw"), "rejected item withheld")
	pending, rejected := withheldStateTally(g)
	assert.Equal(t, 1, pending)
	assert.Equal(t, 1, rejected)
}

// withheldStateTally counts g's withheld items by disposition, via the richer
// withheldItems() accessor. Rejection and retraction are the DECIDED
// dispositions — nothing further is pending about either — and everything else
// withheld is awaiting a human.
func withheldStateTally(g *contentGate) (pending, rejected int) {
	for _, item := range g.withheldItems() {
		switch item.Verdict.Reason {
		case bundles.ReasonRejected, bundles.ReasonRetracted:
			rejected++
		default:
			pending++
		}
	}
	return pending, rejected
}

// TestExecGate_CLIHookTrustThenBlacklist ties the interactive/CLI hook-trust
// mutations to the exec choke: a project-local hook is first-party (passes the
// gate with no review state), and a hook the user rejects via SetBlacklist (the
// `ctxloom blacklist` / [b] path) is withheld by that same gate — the rejection
// beats the local exemption. The gate is rebuilt after the mutation so it reads
// the freshly-persisted store.
func TestExecGate_CLIHookTrustThenBlacklist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hookb.yaml"),
		[]byte("version: \"1.0\"\nhooks:\n  pre_tool:\n    - matcher: Bash\n      command: echo keep\n      type: command\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	// SetBlacklist (backing `ctxloom blacklist`/`bundle reject`) resolves a
	// user-typed ASK, while bundles.Decide reads a producer's canonical ref —
	// so the exec-gate calls below use a SEPARATE, canonical-grammar ref.
	// Both spellings round-trip to the identical
	// trust.Ref{Bundle:"hookb", Kind:KindHook, Name:"pre_tool/0",
	// IsLocal:true} for a bare local name, which is what keeps a CLI
	// rejection reachable by Decide's own parse.
	const cliRef = "hookb#hooks/pre_tool/0"
	declRef := mustLocalItemRef("hookb", trust.KindHook, "pre_tool/0")
	hookPayload, err := (&bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}).ContentPayload()
	require.NoError(t, err)

	// The hook's own bundle, read by the project reader — local posture,
	// project provenance, which is what the first-party step now keys on.
	localRead, err := cfg.BundleLoader().Read("hookb")
	require.NoError(t, err, "the on-disk bundle must resolve")

	// A project-local bundle hook is first-party (no acceptance needed) → passes.
	assert.True(t, bundles.Decide(NewExecutableTrustGate(cfg).Authorizer(), localRead, declRef, hookPayload, bundles.FormRaw).Allow,
		"a first-party local bundle hook must pass the exec gate")

	// CLI rejection → the exec gate withholds it (rejection beats the local
	// exemption).
	_, err = SetBlacklist(cfg, SetBlacklistRequest{Ref: cliRef})
	require.NoError(t, err)
	assert.False(t, bundles.Decide(NewExecutableTrustGate(cfg).Authorizer(), localRead, declRef, hookPayload, bundles.FormRaw).Allow,
		"a CLI-rejected bundle hook must be withheld by the exec gate")
}

// Ensure trust.KindHook participates in the dir/selector grammar end to end.
func TestKindHook_Dir(t *testing.T) {
	assert.Equal(t, "hooks", trust.KindHook.Dir())
	assert.False(t, trust.KindHook.IsContent(), "a hook is an executable surface, not content")
}
