// Table-driven coverage over rootCmd's descendants (Phase 0 of the clifmt
// integration, decision 7 in the CLI-primary reorg plan): every runnable,
// non-hidden command must either render cleanly in all five --format
// encodings, or carry a documented reason it doesn't. New commands are
// caught automatically — formatCoverageWalk fails the test if a command has
// no registry entry, so the registry can't silently go stale.
//
// SCOPE NOTE (read before adding entries): only commands whose RunE already
// calls emit() can meaningfully prove anything here — a command that never
// calls emit() ignores --format entirely today (a pre-existing gap outside
// this task's stated straggler list of sign/llm default/bundle), so
// exercising it across five formats would just prove "doesn't crash",
// trivially true regardless of clifmt. Those are skipped as "not wired to
// emit() yet" rather than fixtured, and are listed in the integration
// report's follow-up section. Commands that ARE wired but need state this
// harness doesn't build (an existing agent/profile/mcp-server/session,
// signed trust, a real remote, a real engine subprocess, a TTY, docker,
// ssh-agent) are also skipped, each with its own reason.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// formatCoverageEntry is one rootCmd descendant's registration: either a
// documented skip, or extraArgs — arguments AFTER the command path itself
// (runFormatCoverageCase prepends the path's own words), as a function of
// --format so a mutating command can target a distinct resource per format
// and avoid collisions across the five sub-runs. A nil extraArgs (with skip
// == "") means the bare command path is the whole invocation.
type formatCoverageEntry struct {
	skip      string
	extraArgs func(format string) []string

	// formatDebt marks a skip entry as T19 debt: this command registers
	// (inherits) the persistent --format flag but its RunE never routes
	// through emit()/cliemit.Emit, so --format is silently accepted and
	// discarded. It is orthogonal to skip's REASON the harness can't
	// exercise the command here (fixture cost, network, ssh-agent,
	// destructive, installer, deprecated-alias-noise) — many skip'd
	// commands DO honor format via emit() and are simply untestable in
	// this harness; formatDebt is false for those.
	//
	// Every formatDebt:true entry must have a matching formatDebtAllowlist
	// entry (enforced by TestFormatCoverage_DebtAllowlistTracksRegistry)
	// naming what removing it requires — see that var's doc comment.
	formatDebt bool
}

// formatCoverageWalk collects every Runnable, non-Hidden command under
// rootCmd as a space-joined path ("bundle list"), matching the keys used in
// the registry below. Hidden commands (completion, the hook/util internals,
// llm serve, container provenance) are excluded here rather than requiring
// a per-command skip entry — "hidden-internal" is structural, not a
// judgment call per command.
func formatCoverageWalk(t *testing.T) []string {
	t.Helper()
	var paths []string
	var walk func(cmd *cobra.Command, prefix string)
	walk = func(cmd *cobra.Command, prefix string) {
		full := strings.TrimSpace(prefix + " " + cmd.Name())
		if cmd.Runnable() && !cmd.Hidden {
			paths = append(paths, full)
		}
		for _, c := range cmd.Commands() {
			walk(c, full)
		}
	}
	for _, c := range rootCmd.Commands() {
		walk(c, "")
	}
	return paths
}

// formatCoverageProject sets up an isolated project root (CTXLOOM_ROOT env,
// undone by t.Cleanup via t.Setenv) with no pre-existing .ctxloom, and
// returns a config.Config over it for fixture setup (operations.CreateBundle
// etc.) — config.Load()'s degraded-fault-tolerance path resolves a config
// cleanly even with nothing on disk yet (built-in LM defaults), so read-only
// commands need no fixture beyond this.
func formatCoverageProject(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(projectroot.EnvVar, dir)
	config.Invalidate()
	cfg, err := config.LoadFresh()
	require.NoError(t, err)
	return cfg
}

// formatCoverageRegistry is the source of truth this test enforces
// completeness against. Skips are grouped by reason; fixtured entries are
// the commands this task's chokepoint work (emit()/format.go) and its named
// stragglers (sign, llm default, bundle create/edit/view/push/export/import/
// distill) directly touched, plus a representative sample of commands that
// were already emit()-wired before this task, to pin no regression.
var formatCoverageRegistry = map[string]formatCoverageEntry{
	// --- exercised: the chokepoint itself + this task's stragglers ---
	"version":     {extraArgs: noExtraArgs},
	"llm list":    {extraArgs: noExtraArgs},
	"config show": {extraArgs: noExtraArgs},
	"config get":  {extraArgs: func(string) []string { return []string{"config"} }},
	"llm default": {extraArgs: noExtraArgs}, // show path; set is exercised directly in llm_default_test.go

	"bundle list": {extraArgs: noExtraArgs},
	"bundle create": {extraArgs: func(f string) []string {
		return []string{"coverage-create-" + f}
	}},
	"bundle view": {extraArgs: func(string) []string {
		return []string{"coverage-target"} // fixture bundle, see setup
	}},
	"bundle edit": {extraArgs: func(string) []string {
		// Same target reused across all five formats: AddTags is create-if-
		// absent, so a repeat add of the same tag is a no-op "no_changes", not
		// an error — safe to call five times running.
		return []string{"coverage-target", "--add-tag", "smoke"}
	}},
	"bundle export": {extraArgs: func(f string) []string {
		dest := filepath.Join(os.TempDir(), "clifmt-coverage-export-"+f+".yaml")
		return []string{"coverage-target", "-o", dest}
	}},
	"bundle import": {extraArgs: func(f string) []string {
		return []string{coverageImportSourcePath(f), "--force"}
	}},
	"bundle distill": {extraArgs: func(string) []string {
		return []string{coverageTargetBundlePath, "--dry-run"}
	}},
	"bundle show": {extraArgs: func(string) []string {
		return []string{"coverage-target"}
	}},

	// --- exercised: a representative sample of pre-existing emit()-wired list commands ---
	"fragment list": {extraArgs: noExtraArgs},
	"command list":  {extraArgs: noExtraArgs},
	"skill list":    {extraArgs: noExtraArgs},
	"agent list":    {extraArgs: noExtraArgs},
	"profile list":  {extraArgs: noExtraArgs},
	"session list":  {extraArgs: noExtraArgs},
	"session query": {extraArgs: func(string) []string {
		// A word that won't match anything in this test's empty/fresh session
		// index; the point here is proving `session query` renders cleanly in
		// all five formats (an empty-rows case, same as an unmatched `session
		// list`), not exercising a real hit — that's session_query_test.go's job.
		return []string{"coverage-query-no-hit"}
	}},
	"remote list":   {extraArgs: noExtraArgs},
	"manage status": {extraArgs: noExtraArgs},
	"doctor":        {extraArgs: noExtraArgs},
	"review":        {extraArgs: func(string) []string { return []string{"--list"} }},
	"search":        {extraArgs: func(string) []string { return []string{"--local", "smoke"} }},

	// --- exercised: real homes of Phase-1 reorg moves (plan Decisions 1-6) ---
	// (the deprecated OLD paths these replace — `signer list`, `manage mcp
	// servers list`, `tooling` — moved to the "deprecated alias" skip group
	// below: cobra's own Deprecated notice prints to stdout ahead of the
	// command's real output, which breaks json/yaml/toml parsing here exactly
	// like the pre-existing `memory *`/`acp agents` deprecated aliases.)
	"trust signer list": {extraArgs: noExtraArgs},
	"mcp server list":   {extraArgs: noExtraArgs},
	"container tooling": {extraArgs: noExtraArgs},

	// --- skip: serve / long-running (structurally not a single rendered result) ---
	"acp":        {skip: "deprecated alias for `acp server`; serves an ACP session, not a single rendered result"},
	"acp server": {skip: "serve: serves an ACP session over stdio for an editor to connect to, not a single rendered result"},
	"acp client": {skip: "requires a configured ACP-type llm label (--llm) and spawns a real third-party ACP-speaking subprocess via the plugin door; covered directly by acp_client_cmd_test.go's stub-Factory tests instead"},
	"mcp":        {skip: "serve: bare `ctxloom mcp` runs the stdio MCP server"},
	"mcp serve":  {skip: "serve: runs the stdio MCP server"},

	// --- skip: streaming (own text/json-only format switch, not emit()) ---
	"session watch": {skip: "streaming: renders one event at a time via its own format switch (see format.go's session/plan watch note), not a single emit() result"},
	"plan watch":    {skip: "streaming: same shape as session watch"},
	"run":           {skip: "streaming + spawns a real engine subprocess: not a single emit() result; run.go's RunE does call emit() on at least one branch (agent-mode payload), not independently re-verified for every branch here"},

	// --- skip: needs a live ssh-agent/git signing identity (non-hermetic) ---
	"sign":                {skip: "requires a live ssh-agent/git identity to discover a signing key; unit-tested directly via runSign()'s DI seam in sign_test.go instead"},
	"bundle sign":         {skip: "deprecated-alias's real home (`ctxloom sign`); same ssh-agent/git identity requirement"},
	"signer add":          {skip: "requires a real public key argument and (without --yes/non-interactive) a confirmation prompt; covered by signer_test.go"},
	"signer show":         {skip: "needs an existing trusted principal (signer add's fixture cost); covered by signer_test.go"},
	"signer remove":       {skip: "destructive; covered by signer_test.go"},
	"trust signer add":    {skip: "same fixture gap as `signer add`, its deprecated alias"},
	"trust signer show":   {skip: "same fixture gap as `signer show`, its deprecated alias"},
	"trust signer remove": {skip: "same fixture gap as `signer remove`, its deprecated alias"},

	// --- skip: deprecated Phase-1 aliases (cobra's own Deprecated notice
	// prints to stdout ahead of the command's real output, breaking
	// json/yaml/toml parsing here — same shape as the pre-existing `memory
	// *`/`acp agents` skips above) ---
	"signer list":             {skip: "deprecated alias for `trust signer list` (plan Decision 1/3); not independently exercised"},
	"manage mcp servers list": {skip: "deprecated alias for `mcp server list` (plan Decision 3); not independently exercised"},
	"tooling":                 {skip: "deprecated alias for `container tooling` (plan Decision 4/6); not independently exercised"},
	"trust":                   {skip: "needs a resolvable, signable ref and trust-store fixture; deprecated bare alias for `trust accept`, not exercised here"},
	"trust accept":            {skip: "needs a resolvable, signable ref and trust-store fixture; not exercised here"},
	"trust reject":            {skip: "needs a resolvable ref; not exercised here"},

	// --- skip: destructive / interactive confirmation, no fixture built here ---
	// T19 audit: all four of these ARE format debt too (bundleDeleteCmd's
	// runBundleDelete, bundle_hold_cli.go's hold/unhold RunEs, and
	// runBundleMCPEdit never call emit() — confirmed absent from the global
	// emit(cmd, ...) call-site grep), even though the reason they're skipped
	// here is the fixture/confirmation cost, not the format bug. `bundle
	// move` is the one command in the surrounding source files that DOES
	// honor format (runBundleMove calls emit()) despite the same "needs a
	// fixture" skip shape — verified NOT debt.
	"bundle delete":   {skip: "destructive + interactive confirm without --force; not exercised here", formatDebt: true},
	"bundle hold":     {skip: "needs an existing pin/lockfile fixture; not exercised here", formatDebt: true},
	"bundle unhold":   {skip: "needs an existing held pin fixture; not exercised here", formatDebt: true},
	"bundle move":     {skip: "needs source/dest bundle layout fixture; not exercised here"},
	"bundle mcp edit": {skip: "needs an existing bundle-scoped MCP entry fixture; not exercised here", formatDebt: true},
	"blacklist":       {skip: "needs a resolvable ref; deprecated alias for `trust reject`, not exercised here"},

	// --- skip: network / real remote required ---
	// T19 audit: 8 of these 9 `remote` commands ARE format debt — none of
	// remote_browse.go/remote_discover.go/remote_update.go/remote_upgrade.go,
	// nor remote.go's add/remove/default/pull RunEs, call emit() (confirmed:
	// zero "emit(" occurrences in those RunE bodies). `remote list` is the
	// lone exception (fixtured above) — so "all nine remote commands" (the
	// original T19 claim) overstates by one; it's 8/9, not 9/9.
	"remote add":      {skip: "network: adds and probes a real remote", formatDebt: true},
	"remote browse":   {skip: "network: browses a real remote's catalog", formatDebt: true},
	"remote default":  {skip: "needs a configured remote fixture", formatDebt: true},
	"remote discover": {skip: "network: queries GitHub for discoverable remotes", formatDebt: true},
	"remote pull":     {skip: "network: clones/fetches a real git remote", formatDebt: true},
	"remote remove":   {skip: "needs a configured remote fixture", formatDebt: true},
	"remote update":   {skip: "network: updates pinned bundle content from a real remote", formatDebt: true},
	"remote upgrade":  {skip: "network: upgrades pinned bundle content from a real remote", formatDebt: true},
	"bundle push":     {skip: "network: publishes to a real remote repository (shares pushBundleCfg with command push, covered by push_sign_test.go)"},
	"command push":    {skip: "network: same pushBundleCfg path as bundle push"},

	// --- skip: docker / container runtime required ---
	// T19 audit: `container check` DOES honor format (containerCheckCmd calls
	// emit()) despite the skip; `container build`/`container scaffold` do
	// not (both write straight to os.Stdout / cmd.OutOrStdout() with no emit
	// call) — format debt.
	"container build":    {skip: "requires a container runtime (docker/podman)", formatDebt: true},
	"container check":    {skip: "probes for a container runtime on the host"},
	"container scaffold": {skip: "writes a container scaffold; not exercised here", formatDebt: true},

	// --- skip: spawns/fans out real agent engines ---
	"weave": {skip: "fans a task across real agent engines/profiles and synthesizes results; not exercised here"},

	// --- skip: side-effecting installers (hooks/statusline/gitignore/mcp registration) ---
	// (`manage init` was deleted outright — not deprecated — root `ctxloom
	// init` is the sole bootstrap, plan Decision 6; no registry entry needed.)
	// T19 audit: every installer/register command below is format debt
	// (confirmed: none of manage.go's install/uninstall/hooks-*/mcp-*/
	// statusline-*/gitignore-install RunEs, nor mcp.go's mcpRegisterCmd/
	// mcpUnregisterCmd inline closures, call emit()) — this is the "eleven
	// commands ignore --format via bare fmt.Printf" class (matches the
	// FINDINGS.md U037-F18 shape). `manage mcp servers show` is the one
	// exception: it shares runMCPShow with `mcp server show`, which does
	// call emit() — not debt, just fixture-gated.
	"manage install":              {skip: "installer: side-effecting project bootstrap"},
	"manage uninstall":            {skip: "installer: side-effecting project teardown"},
	"manage hooks install":        {skip: "installer: writes real hook files"},
	"manage hooks uninstall":      {skip: "installer: removes real hook files"},
	"manage hooks status":         {skip: "reads the hook files the installer above would write; not fixtured here — wired to emit() (shares runManageStatus with `manage status`); registry was stale, not debt"},
	"manage mcp install":          {skip: "deprecated alias for `mcp register` (plan Decision 3); installer: registers ctxloom as an MCP server in editor config"},
	"manage mcp uninstall":        {skip: "deprecated alias for `mcp unregister` (plan Decision 3); installer: unregisters ctxloom as an MCP server"},
	"manage mcp servers add":      {skip: "deprecated alias for `mcp server add`; wired to emit(); mutating, not exercised here"},
	"manage mcp servers remove":   {skip: "deprecated alias for `mcp server remove`; wired to emit(); mutating, not exercised here"},
	"manage mcp servers show":     {skip: "deprecated alias for `mcp server show`; wired to emit(), but needs an existing server fixture; not exercised here"},
	"mcp register":                {skip: "installer: registers ctxloom as an MCP server in editor config (real home of deprecated `manage mcp install`)"},
	"mcp unregister":              {skip: "installer: unregisters ctxloom as an MCP server (real home of deprecated `manage mcp uninstall`)"},
	"mcp server add":              {skip: "wired to emit(); mutating, not exercised here (real home of deprecated `manage mcp servers add`)"},
	"mcp server remove":           {skip: "wired to emit(); mutating, not exercised here (real home of deprecated `manage mcp servers remove`)"},
	"mcp server show":             {skip: "wired to emit(), but needs an existing server fixture; not exercised here (real home of deprecated `manage mcp servers show`)"},
	"manage statusline install":   {skip: "installer: writes real statusline config"},
	"manage statusline uninstall": {skip: "installer: removes real statusline config"},
	"manage gitignore install":    {skip: "installer: writes .gitignore entries"},
	"manage config show":          {skip: "deprecated alias for `config show`; shares runConfigShow, which IS wired to emit() (U035-F07)"},
	"manage config get":           {skip: "deprecated alias for `config get`; shares runConfigGet, which IS wired to emit() (U035-F07)"},
	"manage config edit":          {skip: "deprecated alias for `config edit`; not wired to emit() yet; also opens an editor", formatDebt: true},
	"manage config init":          {skip: "deprecated alias for `config init`; not wired to emit() yet; also an installer", formatDebt: true},
	"config edit":                 {skip: "not wired to emit() yet; also opens an editor (real home of deprecated `manage config edit`)", formatDebt: true},
	"config init":                 {skip: "not wired to emit() yet; also an installer (real home of deprecated `manage config init`)", formatDebt: true},

	// --- skip: acp/mcp entries needing configured agents ---
	"acp entries": {skip: "wired to emit(), but needs a configured ACP agent entry fixture; not exercised here"},
	"acp agents":  {skip: "deprecated alias for `acp entries`; same fixture gap"},

	// --- skip: not wired to emit() yet (pre-existing gap, outside this task's named stragglers) ---
	"fragment show":    {skip: "not wired to emit() yet (item_helpers.go showItem)", formatDebt: true},
	"fragment create":  {skip: "not wired to emit() yet", formatDebt: true},
	"fragment delete":  {skip: "not wired to emit() yet", formatDebt: true},
	"fragment edit":    {skip: "not wired to emit() yet", formatDebt: true},
	"fragment distill": {skip: "not wired to emit() yet", formatDebt: true},
	"command show":     {skip: "not wired to emit() yet", formatDebt: true},
	"command create":   {skip: "not wired to emit() yet", formatDebt: true},
	"command delete":   {skip: "not wired to emit() yet", formatDebt: true},
	"command edit":     {skip: "not wired to emit() yet", formatDebt: true},
	"command distill":  {skip: "not wired to emit() yet", formatDebt: true},
	"skill show":       {skip: "wired to emit(), but needs an existing skill package fixture; not exercised here"},
	"skill create":     {skip: "wired to emit(), but needs an existing skill package fixture; not exercised here"},
	"skill export":     {skip: "wired to emit(), but needs an existing skill package fixture; not exercised here"},
	"skill import":     {skip: "wired to emit(), but needs an existing skill archive fixture; not exercised here"},
	"skill sync":       {skip: "wired to emit(), but needs an existing skill package fixture; not exercised here"},
	"agent show":       {skip: "wired to emit(), but needs an existing agent fixture; not exercised here"},
	"agent set":        {skip: "wired to emit(), but mutating and needs a valid engine/profile fixture; not exercised here"},
	"agent remove":     {skip: "not wired to emit() yet", formatDebt: true},
	"agent default":    {skip: "not wired to emit() yet", formatDebt: true},
	"agent setup":      {skip: "deprecated alias for `init prompt`; not wired to emit() yet", formatDebt: true},
	"init prompt":      {skip: "not wired to emit() yet; also an interactive interview", formatDebt: true},
	"profile show":     {skip: "wired to emit(), but needs an existing profile fixture; not exercised here"},
	"profile create":   {skip: "not wired to emit() yet", formatDebt: true},
	"profile delete":   {skip: "not wired to emit() yet", formatDebt: true},
	"profile edit":     {skip: "not wired to emit() yet", formatDebt: true},
	"profile export":   {skip: "not wired to emit() yet", formatDebt: true},
	"profile import":   {skip: "not wired to emit() yet", formatDebt: true},
	// T19 audit: this entry was WRONG — one of the registry's "known
	// inaccuracies". profileMaterializeCmd's RunE DOES call emit() (verified
	// via find_symbol on profile_materialize.go); it's fixture-gated like
	// `profile show`, not format debt. Reclassified below; not counted in
	// the T19 total and NOT in formatDebtAllowlist.
	"profile materialize": {skip: "wired to emit(), but needs a resolvable profile + --target fixture; not exercised here (corrected T19 audit: previously mislabeled 'not wired to emit() yet', but profileMaterializeCmd's RunE does call emit())"},
	"profile modify":      {skip: "not wired to emit() yet", formatDebt: true},
	"session show":        {skip: "wired to emit(), but needs an existing session fixture; not exercised here"},
	"session rename":      {skip: "not confirmed wired; needs an existing session fixture; not exercised here; T19 audit confirmed NOT wired: session_cmd.go's rename RunE is an inline closure that never calls emit()", formatDebt: true},
	"session forget":      {skip: "not confirmed wired; destructive; not exercised here; T19 audit confirmed NOT wired: session_cmd.go's forget RunE is an inline closure that never calls emit()", formatDebt: true},
	"session distill":     {skip: "not confirmed wired; needs an existing session fixture; not exercised here; T19 audit confirmed NOT wired: runSessionDistill never calls emit()", formatDebt: true},
	"session backfill":    {skip: "wired to emit(), but needs an existing session fixture; covered directly by session_backfill_test.go instead"},
	"config":              {skip: "not wired to emit() yet; NOTE: bare `config` has no RunE and is not cobra-Runnable, so formatCoverageWalk never actually visits it — this entry is inert/dead and not counted toward the T19 total"},
	"init":                {skip: "interactive bootstrap interview; not exercised here; T19 audit confirmed NOT wired: runInit never calls emit()", formatDebt: true},
}

// noExtraArgs is the extraArgs value for a command whose full invocation is
// just its own path (a plain "list"/"show"/status-style query).
func noExtraArgs(string) []string { return nil }

// coverageTargetBundlePath is filled in by the outer test after creating the
// shared "coverage-target" fixture bundle, since bundle distill takes a file
// PATH (glob pattern), not a bundle name.
var coverageTargetBundlePath string

// coverageImportSourcePath writes (once per format, since bundle import
// names its destination from the source YAML's own bundle name and reuses
// it with --force) a small standalone bundle YAML importable independent of
// the "coverage-target" fixture, and returns its path.
func coverageImportSourcePath(format string) string {
	dir := os.TempDir()
	path := filepath.Join(dir, "clifmt-coverage-import-"+format+".yaml")
	name := "coverage-import-" + format
	body := fmt.Sprintf("version: \"1.0.0\"\ndescription: %s\nfragments:\n  ex:\n    content: hi\n    no_distill: true\n", name)
	_ = os.WriteFile(path, []byte(body), 0o644)
	return path
}

// TestFormatCoverage_AllRootCmdDescendants is the table-driven test the
// clifmt integration task requires: every rootCmd descendant either renders
// in all five --format encodings without error, or is allowlisted with a
// documented reason. Discovery is structural (formatCoverageWalk), so a
// future command with no registry entry fails the test loudly instead of
// silently going unchecked.
func TestFormatCoverage_AllRootCmdDescendants(t *testing.T) {
	cfg := formatCoverageProject(t)

	// The shared read fixture behind bundle view/edit/export/distill.
	ctx := context.Background()
	_, err := operations.CreateBundle(ctx, cfg, operations.CreateBundleRequest{
		Name:        "coverage-target",
		Description: "clifmt format-coverage fixture",
		Fragments: map[string]operations.BundleFragmentInput{
			"example": {Content: "hello from the coverage fixture", NoDistill: true},
		},
	})
	require.NoError(t, err, "seed the shared coverage-target bundle fixture")
	coverageTargetBundlePath = cfg.GetBundleDirs()[0] + "/coverage-target.yaml"

	paths := formatCoverageWalk(t)
	require.NotEmpty(t, paths, "rootCmd must have runnable descendants")

	for _, path := range paths {
		entry, ok := formatCoverageRegistry[path]
		if !ok {
			t.Errorf("command %q has no formatCoverageRegistry entry (fixture or documented skip) — add one", path)
			continue
		}
		t.Run(path, func(t *testing.T) {
			if entry.skip != "" {
				t.Skip(entry.skip)
			}
			require.NotNil(t, entry.extraArgs, "registered entry for %q must set either skip or extraArgs", path)
			for _, format := range []string{"text", "json", "yaml", "toml", "markdown"} {
				t.Run(format, func(t *testing.T) {
					args := append(strings.Fields(path), entry.extraArgs(format)...)
					runFormatCoverageCase(t, path, args, format)
				})
			}
		})
	}
}

// runFormatCoverageCase drives the real cobra command tree exactly as a
// user's shell invocation would (rootCmd.SetArgs + Execute), so the
// persistent --format flag resolves through cobra's own inherited-flag
// machinery rather than a hand-rolled shortcut. It asserts no error and that
// non-text formats produced syntactically valid output in their encoding.
func runFormatCoverageCase(t *testing.T, path string, args []string, format string) {
	t.Helper()
	full := append(append([]string{}, args...), "--format", format)
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(full)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	err := rootCmd.Execute()
	require.NoError(t, err, "ctxloom %s (stderr: %s)", strings.Join(full, " "), errOut.String())

	switch clifmt.Format(format) {
	case clifmt.FormatJSON:
		assert.Truef(t, json.Valid(out.Bytes()), "invalid JSON for %q:\n%s", path, out.String())
	case clifmt.FormatYAML:
		var v any
		assert.NoErrorf(t, yaml.Unmarshal(out.Bytes(), &v), "invalid YAML for %q:\n%s", path, out.String())
	case clifmt.FormatTOML:
		var v map[string]any
		assert.NoErrorf(t, toml.Unmarshal(out.Bytes(), &v), "invalid TOML for %q:\n%s", path, out.String())
	default: // text, markdown: no fixed grammar to validate against, just non-panicking output
	}
}

// formatDebtAllowlist is T19's ("--format is declared far more widely than it
// is honored", FINDINGS.md) enforcement ledger. It is the machine-readable
// list of commands currently allowed to register/inherit the persistent
// --format flag and silently discard it (their RunE never reaches
// emit()/cliemit.Emit). TestFormatCoverage_DebtAllowlistTracksRegistry below
// requires every formatCoverageRegistry entry marked formatDebt: true to have
// exactly one matching key here, and vice versa — so a new broken command (or
// a stale allowlist entry left behind after a fix) fails the build instead of
// drifting silently.
//
// TO PAY DOWN ONE ENTRY: fix the named command's RunE to route its output
// through emit() (see any fixtured entry above, e.g. `bundle create`, for the
// pattern), delete that command's line from this map, AND flip its
// formatCoverageRegistry entry's formatDebt to false (or, if the command can
// now be safely fixtured and exercised here, move it up into a real
// extraArgs entry instead of a skip). Each entry is independent — removing
// one never requires touching another, so flow batches that own different
// commands can land in parallel.
//
// Grouped by owning surface per the T19 remediation batches (gooey-basil owns
// rendering; brave-mango/sick-shawl/known-bleep/clean-pony own the commands).
var formatDebtAllowlist = map[string]string{
	// --- config surface (config.go; manage.go's deprecated aliases share the same RunEs) ---
	// `config show`/`config get` (and their two aliases) were paid down by
	// U035-F07: both RunEs route through emit() over a yaml-round-tripped
	// payload, so all five encodings carry the real configuration.
	"config edit":        "config.go: runConfigEdit must route through emit() (or be reclassified as structurally exempt: it only launches $EDITOR, no renderable result)",
	"config init":        "config.go: runConfigInit must route through emit() instead of a bare fmt.Fprintf",
	"manage config edit": "deprecated alias of `config edit`; shares runConfigEdit — paid down by the same fix",
	"manage config init": "deprecated alias of `config init`; shares runConfigInit — paid down by the same fix",

	// --- remote surface (remote.go, remote_browse.go, remote_discover.go, remote_update.go, remote_upgrade.go) ---
	"remote add":      "remote.go: remoteAddCmd's inline RunE must route through emit() instead of fmt.Printf",
	"remote remove":   "remote.go: remoteRemoveCmd's inline RunE must route through emit() instead of fmt.Printf",
	"remote default":  "remote.go: runRemoteDefault must route through emit() instead of fmt.Println/fmt.Printf",
	"remote pull":     "remote.go: remotePullCmd's inline RunE + renderPullSummary must route through emit()",
	"remote browse":   "remote_browse.go: runRemoteBrowse must route through emit()",
	"remote discover": "remote_discover.go: the inline RunE (interactive add flow) must route through emit()",
	"remote update":   "remote_update.go: runRemoteUpdate must route through emit()",
	"remote upgrade":  "remote_upgrade.go: runRemoteUpgrade must route through emit()",

	// --- bundle destructive/fixture-gated surface (bundle_edit.go, bundle_hold_cli.go, bundle_items.go) ---
	"bundle delete":   "bundle_edit.go: runBundleDelete must route through emit()",
	"bundle hold":     "bundle_hold_cli.go: the hold RunE must route through emit()",
	"bundle unhold":   "bundle_hold_cli.go: the unhold RunE must route through emit()",
	"bundle mcp edit": "bundle_items.go: runBundleMCPEdit must route through emit()",

	// --- container surface (container_cmd.go) ---
	"container build":    "container_cmd.go: containerBuildCmd's inline RunE streams to os.Stdout directly; must route its final result through emit()",
	"container scaffold": "container_cmd.go: containerScaffoldCmd's inline RunE must route through emit() instead of iox.ErrWriter Printf calls",

	// --- fragment/command item surfaces (fragment.go, command_cmd.go, item_helpers.go) ---
	"fragment show":    "item_helpers.go: showItem (used by fragment show) must route through emit()",
	"fragment create":  "fragment.go: the create RunE must route through emit()",
	"fragment delete":  "fragment.go: the delete RunE must route through emit()",
	"fragment edit":    "fragment.go: the edit RunE must route through emit()",
	"fragment distill": "fragment.go: the distill RunE must route through emit()",
	"command show":     "command_cmd.go: the show RunE must route through emit()",
	"command create":   "command_cmd.go: the create RunE must route through emit()",
	"command delete":   "command_cmd.go: the delete RunE must route through emit()",
	"command edit":     "command_cmd.go: the edit RunE must route through emit()",
	"command distill":  "command_cmd.go: the distill RunE must route through emit()",

	// --- agent/init surfaces (agent.go, init.go) ---
	"agent remove":  "agent.go: the agent-remove RunE must route through emit()",
	"agent default": "agent.go: the agent-default RunE must route through emit()",
	"agent setup":   "deprecated alias of `init prompt`; shares runSetupPromptCmd — paid down by the same fix",
	"init prompt":   "init.go: runSetupPromptCmd must route through emit() (also an interactive interview — may warrant a structural-exemption reclassification instead)",
	"init":          "init.go: runInit must route through emit() (also an interactive bootstrap — may warrant a structural-exemption reclassification instead)",

	// --- profile surface (profile.go) ---
	"profile create": "profile.go: the create RunE must route through emit()",
	"profile delete": "profile.go: the delete RunE must route through emit()",
	"profile edit":   "profile.go: the edit RunE must route through emit()",
	"profile export": "profile.go: the export RunE must route through emit()",
	"profile import": "profile.go: the import RunE must route through emit()",
	"profile modify": "profile.go: the modify RunE must route through emit()",

	// --- session surface (session_cmd.go) ---
	"session rename":  "session_cmd.go: the rename RunE (inline closure) must route through emit()",
	"session forget":  "session_cmd.go: the forget RunE (inline closure) must route through emit()",
	"session distill": "session_cmd.go: runSessionDistill must route through emit()",
}

// TestFormatCoverage_DebtAllowlistTracksRegistry is T19's enforcing half: it
// fails if formatCoverageRegistry's formatDebt markers and
// formatDebtAllowlist's keys ever drift apart, in either direction —
//   - a formatDebt:true entry with no allowlist key (new debt introduced
//     without being counted), and
//   - an allowlist key whose registry entry isn't (or is no longer) marked
//     formatDebt (a stale entry: the underlying command was fixed, or
//     reclassified, and this ledger line was never removed).
//
// It also catches allowlist keys that don't correspond to any registered
// command at all (renamed/removed commands leaving orphaned debt).
func TestFormatCoverage_DebtAllowlistTracksRegistry(t *testing.T) {
	for path, entry := range formatCoverageRegistry {
		_, allowlisted := formatDebtAllowlist[path]
		switch {
		case entry.formatDebt && !allowlisted:
			t.Errorf("command %q is marked formatDebt in formatCoverageRegistry but has no formatDebtAllowlist entry — add one naming the required fix", path)
		case !entry.formatDebt && allowlisted:
			t.Errorf("command %q has a formatDebtAllowlist entry but its formatCoverageRegistry entry isn't marked formatDebt — stale allowlist entry (debt already paid?); remove it", path)
		}
	}
	for path, reason := range formatDebtAllowlist {
		require.NotEmpty(t, reason, "formatDebtAllowlist[%q] needs a non-empty reason", path)
		if _, ok := formatCoverageRegistry[path]; !ok {
			t.Errorf("formatDebtAllowlist entry %q has no formatCoverageRegistry entry at all (renamed or removed command?)", path)
		}
	}
}
