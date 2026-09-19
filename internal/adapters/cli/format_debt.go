package cli

// This ledger lives in PRODUCTION code, not beside the coverage test that also
// reads it, and that placement is load-bearing rather than tidiness.
//
// The guard used to fire in PersistentPostRunE, keyed off a runtime flag that
// emit() sets — which can only be known AFTER RunE has run. For a read-only
// command that is untidy; for a mutating one it is a defect with teeth. Measured:
//
//	$ ctxloom profile create bug --parent <p> --format json
//	Created profile "bug" ... / Saved to: .../profiles/bug.yaml
//	{ "error": "... does not support it yet ..." }   EXIT=1
//	$ ls .ctxloom/profiles/   ->   bug.yaml   <-- created anyway
//
// Refusing BEFORE RunE requires knowing in advance which commands carry debt,
// and this map is the only thing that knows. A test file cannot tell production
// code anything, so it had to move here. The coverage test still enforces it
// against formatCoverageRegistry from the test side; nothing about that changed.

// formatDebtAllowlist is the enforcement ledger for "--format is declared far
// more widely than it is honored". It is the machine-readable
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
// Grouped by owning surface, each surface paid down by a separate batch.
var formatDebtAllowlist = map[string]string{
	// --- config surface (config.go) ---
	// `config show`/`config get` were paid down:
	// both RunEs route through emit() over a yaml-round-tripped
	// payload, so all five encodings carry the real configuration.
	"config edit":   "config.go: runConfigEdit must route through emit() (or be reclassified as structurally exempt: it only launches $EDITOR, no renderable result)",
	"config create": "config.go: runConfigCreate must route through emit() instead of a bare fmt.Fprintf",

	// --- remote surface (remote.go, remote_browse.go, remote_discover.go, remote_update.go, remote_upgrade.go) ---
	// `remote remove` (runRemoteRemove) was paid down alongside the report/
	// --yes safety-posture rewrite: both its report and --yes branches now
	// route through emit().
	"remote create":   "remote.go: remoteCreateCmd's inline RunE must route through emit() instead of fmt.Printf",
	"remote default":  "remote.go: runRemoteDefault must route through emit() instead of fmt.Println/fmt.Printf",
	"deps pull":       "deps_pull.go: runDepsPull's RunE + renderPullSummary must route through emit()",
	"remote show":     "remote_browse.go: runRemoteBrowse must route through emit()",
	"remote discover": "remote_discover.go: the inline RunE (interactive add flow) must route through emit()",
	"deps check":      "deps_check.go: runDepsCheck must route through emit()",
	"deps upgrade":    "deps_upgrade.go: runDepsUpgrade must route through emit()",

	// --- bundle destructive/fixture-gated surface (bundle_edit.go, bundle_hold_cli.go, bundle_items.go) ---
	// `bundle remove` (runBundleRemove) was paid down alongside the report/
	// --yes safety-posture rewrite: both its report and --yes branches now
	// route through emit().
	"deps hold":       "deps.go: runDepsHold must route through emit()",
	"deps unhold":     "deps.go: runDepsUnhold must route through emit()",
	"mcp server edit": "bundle_items.go: runBundleMCPEdit must route through emit()",

	// --- container surface (container_cmd.go) ---
	"container build":    "container_cmd.go: containerBuildCmd's inline RunE streams to os.Stdout directly; must route its final result through emit()",
	"container scaffold": "container_cmd.go: containerScaffoldCmd's inline RunE must route through emit() instead of iox.ErrWriter Printf calls",

	// --- fragment/command item surfaces (fragment.go, command_cmd.go, item_helpers.go) ---
	"fragment show": "item_helpers.go: showItem (used by fragment show) must route through emit()",
	"command show":  "item_helpers.go: showItem (used by command show) must route through emit()",

	// --- agent/init surfaces (agent.go, init.go) ---
	// `agent remove` (runAgentRemove) was paid down alongside the report/
	// --yes safety-posture rewrite: both its report and --yes branches now
	// route through emit().
	"agent default": "agent.go: the agent-default RunE must route through emit()",
	"agent setup":   "deprecated alias of `init prompt`; shares runSetupPromptCmd — paid down by the same fix",
	"init prompt":   "init.go: runSetupPromptCmd must route through emit() (also an interactive interview — may warrant a structural-exemption reclassification instead)",
	"init":          "init.go: runInit must route through emit() (also an interactive bootstrap — may warrant a structural-exemption reclassification instead)",

	// --- profile surface (profile.go) ---
	// `profile remove` (runProfileRemove) was paid down alongside the
	// report/--yes safety-posture rewrite: both its report and --yes
	// branches now route through emit().
	"profile create": "profile.go: the create RunE must route through emit()",
	"profile edit":   "profile.go: the edit RunE must route through emit()",
	"profile export": "profile.go: the export RunE must route through emit()",
	"profile import": "profile.go: the import RunE must route through emit()",
	"profile modify": "profile.go: the modify RunE must route through emit()",

	// --- session surface (session_cmd.go) ---
	"session distill": "session_cmd.go: runSessionDistill must route through emit()",
}
