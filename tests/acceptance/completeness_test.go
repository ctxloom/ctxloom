//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/cli"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// requiredHiddenLeaves are machine callbacks (ctxloom hook ...) that run on
// EVERY session but are registered Hidden: true, so leafCommands' walk never
// emits them (a Hidden command fails the `!c.Hidden` gate that admits a node
// to the walk's output, regardless of whether it's actually a runnable
// leaf) — the completeness gate structurally could not flag them as
// uncovered. Listed explicitly here so they're checked the same as any other
// leaf. They are expected to show RED until real scenarios exist for them;
// that redness is the fix, not a regression to paper over.
var requiredHiddenLeaves = []string{
	"ctxloom hook inject-context",
	"ctxloom hook session-bind",
	"ctxloom hook stamp-plan",
	"ctxloom hook hud",
}

// engineMatrixLeaves are CLI leaves parameterized by --engine whose per-engine
// path (claude-code vs. codex vs. kiro vs. antigravity) can rot silently
// behind a single scenario that only ever exercises one engine value: a bare
// substring match on the leaf's command path is satisfied forever by one
// "--engine claude-code" scenario, so the other three engines' install/init
// paths get no red signal when they break. Each listed leaf requires a
// genuine "I run" invocation per engine, not just one for the whole leaf.
var engineMatrixLeaves = map[string][]string{
	"ctxloom manage install": {"claude-code", "codex", "kiro", "antigravity"},
	// Repointed by the verb-spine reorg: `manage config init` was a deprecated
	// alias and is DELETED, so the matrix rides its canonical twin.
	"ctxloom config init": {"claude-code", "codex", "kiro", "antigravity"},
}

// strictRunLeaves are ordinary (visible, non-engine-matrix) leaves promoted
// from the substring containsLeafPath check to the stricter ranAsCommand
// ("I run") check because the command string ALSO appears as inert prose
// elsewhere in the corpus: `ctxloom doctor` is quoted inside the
// "ctxloom-doctor" Agent Skill body in steps_j10_doctor.go, so a bare
// substring match would credit that vacuous mention as coverage and let the
// gate stay green even after the real coverer (doctor.feature) is gone. Listed
// here so only a genuine `I run "<leaf>"` invocation counts — the same
// hardening already applied to the hidden and engine-matrix leaves.
var strictRunLeaves = []string{
	"ctxloom doctor",
}

// ranAsCommand reports whether path was actually invoked by a scenario (a
// genuine "When/And I run "<path>..."" step), as opposed to merely
// appearing as a substring somewhere in the corpus — e.g. quoted inside an
// unrelated assertion like `the file "settings.json" contains "ctxloom hook
// session-bind"`, which mentions the string without ever running the
// command. Substring-only matching is exactly the vacuous-coverage failure
// mode this gate exists to catch, so the required-hidden-leaf and
// engine-matrix checks (the newly-honest parts of this gate) use this
// stricter form instead of the plain corpus.Contains used elsewhere.
func ranAsCommand(corpus, path string) bool {
	return strings.Contains(corpus, `I run "`+path)
}

// ranAsTool reports whether an MCP tool was actually invoked by a scenario (a
// genuine `the agent calls tool "<name>"` step — see steps_mcp.go), as
// opposed to merely being named somewhere in the corpus. Plain substring
// matching over tool names is the same vacuous-coverage hole ranAsCommand
// closes for CLI leaves: mcp_tools.feature used to preface its scenarios with
// prose naming compact_session/get_previous_session as explicitly NOT
// covered, which nonetheless satisfied a bare strings.Contains check and hid
// the gap. Every real invocation in this suite goes through callTool via one
// of the two `calls tool "<name>"` step patterns, so that literal substring
// is the correct, exact signal.
func ranAsTool(corpus, name string) bool {
	return strings.Contains(corpus, `calls tool "`+name+`"`)
}

// containsLeafPath is the ordinary (non-"I run") leaf check's substring test,
// hardened against a PREFIX collision: "ctxloom sign" is a literal prefix of
// "ctxloom signer add"/"ctxloom signer remove" (distinct leaves), so a bare
// strings.Contains credited "ctxloom sign" with coverage the moment J3
// (steps_j3.go) started mentioning the signer leaves in comments — a false
// positive that would have silently pruned an actually-uncovered leaf from
// the allowlist (exactly the vacuous-coverage failure mode this gate exists
// to catch). A match only counts when the byte right after it is absent or
// not itself an identifier character, so "ctxloom sign" no longer matches
// inside "ctxloom signer ...".
func containsLeafPath(corpus, path string) bool {
	for idx := 0; ; {
		i := strings.Index(corpus[idx:], path)
		if i < 0 {
			return false
		}
		end := idx + i + len(path)
		if end >= len(corpus) || !isLeafIdentByte(corpus[end]) {
			return true
		}
		idx += i + 1
	}
}

func isLeafIdentByte(b byte) bool {
	return b == '_' || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z') || ('0' <= b && b <= '9')
}

// knownUncoveredCLI is the EXACT set of CLI leaves (bare leaves, hidden
// machine callbacks, and "<leaf> --engine <name>" variants) this gate accepts
// as uncovered today. This is not a cap or a silencer: TestCompleteness
// compares the ACTUAL uncovered set against this one and fails on any
// difference in EITHER direction — something new going uncovered (a
// regression) or an entry here becoming covered (this list has rotted and
// must be pruned back down). Every entry names the task that will backfill
// it; that task is the only legitimate way an entry leaves this list.
var knownUncoveredCLI = []string{
	// The publisher-signing surface is now covered by J18 (steps_j18_signing.go,
	// j18_signing.feature): `ctxloom bundle sign` (bare ref, --all, an item ref,
	// and the empty-publish-set failure), `ctxloom trust signer list`,
	// `trust signer create|show|delete`, `trust accept`, `trust reject`, and
	// `bundle move` (verbatim relocation plus the refused-move path). Every one
	// runs against a real ssh-agent (testenv.StartSSHAgent) and asserts payload:
	// each `.sig` is verified independently against bundle bytes read fresh off
	// disk, `--all` is counted against the publish set, and `--project`'s store
	// location is asserted on BOTH the project and the user path.
	//
	// "ctxloom trust signer create"/"ctxloom trust signer delete" left this
	// list earlier: J3 (steps_j3.go, j3_corporate_signed.feature) drives both —
	// the Background's "Alice trusts the company key" step runs
	// `ctxloom trust signer create ... --project`, and scenario 6's "Alice
	// revokes her trust in the company key" runs
	// `ctxloom trust signer delete ... --project`. "ctxloom trust signer show"
	// left it when J7 (steps_j7.go, j7_incident.feature)'s
	// irrevocable-embedded-key scenario started driving it before and after the
	// removal attempt.
	//
	// Long-lived watcher with no bounded/hermetic exit in this harness yet.
	// Backfill: task cheap-pug.
	"ctxloom session watch",
	// Hidden machine callbacks that run on every session (SessionStart/tool
	// hooks) but have no scenario driving their stdin payload directly.
	// Backfill: task weary-crowd.
	"ctxloom hook inject-context",
	"ctxloom hook session-bind",
	"ctxloom hook stamp-plan",
	"ctxloom hook hud",
	// Post-reorg coverage debt, now MUCH smaller: the verb-spine reorg deleted
	// every deprecated alias, so the ~15 canonical leaves whose only coverage
	// used to ride an alias (`blacklist`, bare `trust`, `signer *`, `tooling`,
	// `acp agents`, `manage config *`, `manage mcp *`) were re-spelled onto
	// their canonical leaves in the same change and left this list. What
	// remains is genuinely uncovered. Backfill: one task.
	"ctxloom acp client",
	"ctxloom acp serve",
	// `ctxloom session search` was pruned from this list when
	// j23_recall.feature landed, because THIS GATE READS THE FEATURE CORPUS AS
	// TEXT and its scenarios drive the leaf — but read that credit narrowly:
	// every scenario in j23_recall.feature is @wip and therefore excluded from
	// the default run, so the leaf is SPECIFIED rather than exercised today.
	// The gate has no notion of @wip and cannot make that distinction itself;
	// this comment is where the distinction lives. Real execution arrives when
	// those scenarios are untagged one at a time, which is the workflow that
	// file was written for.
	// `mcp server edit` is the new home of the deleted `bundle mcp edit`; it
	// opens $EDITOR on a bundle-scoped MCP entry, which the harness has no
	// fixture for (its predecessor was `excludedLeaves`-excluded for exactly
	// that reason).
	"ctxloom mcp server edit",
	// Engine-matrix variants: only the claude-code path is exercised;
	// codex/kiro/antigravity need their own fixtures. Backfill: task glad-skid.
	"ctxloom manage install --engine codex",
	"ctxloom manage install --engine kiro",
	"ctxloom manage install --engine antigravity",
	"ctxloom config init --engine codex",
	"ctxloom config init --engine kiro",
	"ctxloom config init --engine antigravity",
}

// knownUncoveredTools is knownUncoveredCLI's MCP-tool counterpart: the exact
// set of registered tools (from a plain, non-forwarding `mcp serve`) this
// gate accepts as uncovered, checked for exact-set equality the same way.
var knownUncoveredTools = []string{
	// Agent-delegation bus + trigger evaluation: agent_run is exercised by J6
	// (steps_j6_delegation.go, j6_delegation.feature — a coordinator spawning
	// delegated children and auditing their journaled privilege grant).
	// agent_send/agent_recv are now exercised by J17
	// (steps_j17_cross_engine_delegation.go, j17_cross_engine_delegation.feature
	// — the real two-way bus, both directions, content asserted on each side).
	// agent_stop is now exercised by J6's failure-path scenario: idempotent
	// double-stop plus a refused stop on a run id that was never spawned (a
	// real regression this scenario now pins). Trigger evaluation remains
	// unexercised. Backfill: task spry-niece.
	"evaluate_triggers",
	// Session-memory tools named only in mcp_tools.feature's prose, never
	// actually invoked (see ranAsTool's doc comment). Backfill: task spry-niece.
	"compact_session",
	"get_previous_session",
	"list_sessions",
}

// knownUncoveredRunnerOnlyTools is the exact set of tools that exist ONLY on
// the documented (runner-terminated, cli.NewDocMCPServer) MCP surface -- not
// on the standalone `ctxloom mcp serve` surface knownUncoveredTools governs
// -- and are not yet exercised by any scenario. roster,
// agent_report, and agent_fetch_artifact are named explicitly in
// scripts/gendocs/main.go's mcpIntro as existing only on this surface;
// before this list and its subtest existed, they had no completeness
// coverage of any kind. Backfill: task spry-niece (same task as above).
var knownUncoveredRunnerOnlyTools = []string{
	"agent_fetch_artifact",
	"agent_report",
	"roster",
}

// assertExactUncovered fails t if got (the actual uncovered set, need not be
// pre-sorted) differs from want (a knownUncovered* allowlist) in either
// direction: an item in got but not want is a NEW gap (something regressed);
// an item in want but not got means the allowlist is stale (it was backfilled
// and should be pruned). label names the surface for the failure message.
//
// Returns len(gotSet): the ACTUAL number of uncovered surfaces observed this
// run (not the allowlist's own size), so a caller can fold it into
// TestCompleteness's total-deficit report and ratchet. Deliberately not
// len(wantSet) — if got and want ever disagree (a currently-failing run),
// the live count is the honest one; the allowlist's book-keeping isn't.
func assertExactUncovered(t *testing.T, label string, got, want []string) int {
	t.Helper()
	gotSet := append([]string{}, got...)
	wantSet := append([]string{}, want...)
	sort.Strings(gotSet)
	sort.Strings(wantSet)

	wantIdx := make(map[string]bool, len(wantSet))
	for _, w := range wantSet {
		wantIdx[w] = true
	}
	gotIdx := make(map[string]bool, len(gotSet))
	for _, g := range gotSet {
		gotIdx[g] = true
	}

	for _, g := range gotSet {
		if !wantIdx[g] {
			t.Errorf("%s: newly uncovered %q — not in the allowlist (a real regression, or add it with a backfill task)", label, g)
		} else {
			t.Logf("%s: allowlisted uncovered — %s", label, g)
		}
	}
	for _, w := range wantSet {
		if !gotIdx[w] {
			t.Errorf("%s: allowlist entry %q is now covered — prune it from knownUncovered*", label, w)
		}
	}

	// Prominent, single-line deficit statement — the per-item t.Logf lines
	// above are detail, not a substitute for this. A reader skimming output
	// (or `just` failure summaries, which truncate) should not have to count
	// "allowlisted uncovered" lines to learn the size of the gap.
	t.Logf("%s: %d ALLOWLISTED UNCOVERED (debt, not a pass) — %v", label, len(gotSet), gotSet)
	return len(gotSet)
}

// excludedLeaves are public-surface entry points deliberately not exercised by a
// scenario, each with the reason. Printed on every run so the exclusion is never
// silent (see plan §7.5 / "no silent caps").
var excludedLeaves = map[string]string{
	"ctxloom config edit": "opens $EDITOR on config.yaml; TTY-only, no hermetic fixture",
	"ctxloom mcp serve":   "the MCP server itself; exercised by every @mcp scenario",
	"ctxloom llm serve":          "internal gRPC plugin server",
	"ctxloom remote discover":    "network discovery search; no deterministic fixture (excluded)",
	// Remote ops needing richer state than a single-commit fixture provides. The
	// core clone/fetch/install/sync path is covered hermetically by the @remote
	// content scenarios (browse/install/sync/lock against a seeded file:// repo).
	"ctxloom remote update":         "checks installed bundles for a newer SHA; needs a second remote commit. Core fetch covered by @remote scenarios",
	"ctxloom remote upgrade":        "upgrades installed bundles to latest; needs an update cycle. Core fetch covered by @remote scenarios",
	"ctxloom session distill":       "@live + requires a real backend session transcript; the fragment/command/bundle distill paths cover the distiller",
	"ctxloom completion zsh":        "shell variant; the completion path is covered via bash",
	"ctxloom completion fish":       "shell variant; the completion path is covered via bash",
	"ctxloom completion powershell": "shell variant; the completion path is covered via bash",
	"ctxloom help":                  "cobra-generated help command",
	"ctxloom bundle push":           "publishes to a remote forge (push/PR); requires a writable remote, out of hermetic scope",
	"ctxloom container build":       "builds the agent container image; requires a container runtime + network pulls, out of hermetic scope",
	"ctxloom plan watch":            "long-lived file watcher (runs until interrupted); no hermetic exit",
}

// excludedTools are registered MCP tools no hermetic scenario reaches.
//
// compact_session and get_previous_session used to be listed here too, with
// justifications that turned out to be unearned: compact_session's claimed
// "exercised at the operations layer in tests/integration" but grep finds no
// Compact/Recover reference there at all, and get_previous_session's claim of
// alternate coverage never existed anywhere. Both are now genuinely
// uncovered — a real gap, not a documented, accepted one — and show RED
// below rather than being quietly excluded. Tracked as a follow-up (Phase 0
// only fixes the gate; it doesn't backfill the missing scenarios).
//
// recover_session used to be listed here too ("requires a real backend
// transcript"), which was also unearned: a MOCK backend plus a directly
// seeded canonical transcript.jsonl (see steps_recover_session.go) reaches it
// hermetically, no real backend needed. A flow-level regression
// scenario in mcp_tools.feature now drives it for real — pruned.
var excludedTools = map[string]string{}

// excludedTemplates are resource templates with no hermetic scenario. Currently
// none — every templated resource is exercised, including remotes/{name}/contents
// against a seeded file:// remote.
var excludedTemplates = map[string]string{}

// maxKnownUncoveredTotal is a RATCHET on the combined size of every
// knownUncovered* allowlist (currently 15 CLI leaves + 4 MCP tools + 3
// runner-only MCP tools = 22). It exists because assertExactUncovered's
// exact-set check only catches a leaf/tool going uncovered WITHOUT anyone
// updating the matching allowlist — it does nothing to stop someone from
// adding a new gap AND an allowlist entry for it in the same change, which
// passes cleanly today with only a t.Logf to notice by. This constant makes
// that silent-absorption path loud: TestCompleteness fails if the live total
// ever exceeds it, so growing the allowlists requires bumping this number in
// the same diff, with a reason, in code review.
//
// It does NOT fail on the existing 22 (that's accepted, tracked debt — every
// entry in the three lists above carries its own backfill-task comment) and
// it is not automatically lowered by backfill work: when a task closes gaps,
// lower this number too, or the ratchet just grows slack instead of tracking
// the debt down.
//
// LOWERED from 43 by the verb-spine reorg: deleting the deprecated aliases
// forced the ~15 canonical leaves that were covered only through an alias to
// be re-spelled onto their canonical spelling, which is what actually closed
// the gaps. Lowering the ceiling in the same change is the point — a reorg
// that shrank the debt while leaving the ratchet at 43 would just bank slack.
const maxKnownUncoveredTotal = 22

// TestCompleteness enforces that every public CLI leaf, MCP tool, and MCP
// resource is exercised by some scenario or step, or is explicitly excluded.
func TestCompleteness(t *testing.T) {
	corpus := loadCorpus(t)

	// Populated by the three allowlisted subtests below, then folded into one
	// prominent deficit statement (and ratcheted) once they've all run.
	var cliUncovered, toolsUncovered, runnerOnlyUncovered int

	t.Run("cli leaves", func(t *testing.T) {
		// requiredHiddenLeaves are appended rather than discovered by
		// leafCommands: Hidden commands fail that walk's admission gate, which
		// is the exact structural blind spot this list exists to close.
		leaves := append([]string{}, leafCommands(cli.GetRootCmd())...)
		leaves = append(leaves, requiredHiddenLeaves...)
		sort.Strings(leaves)

		var uncovered []string
		for _, path := range leaves {
			if reason, ok := excludedLeaves[path]; ok {
				t.Logf("excluded: %s — %s", path, reason)
				continue
			}
			if engines, ok := engineMatrixLeaves[path]; ok {
				for _, engine := range engines {
					invocation := path + " --engine " + engine
					if !ranAsCommand(corpus, invocation) {
						uncovered = append(uncovered, invocation)
					}
				}
				continue
			}
			if slices.Contains(requiredHiddenLeaves, path) {
				if !ranAsCommand(corpus, path) {
					uncovered = append(uncovered, path)
				}
				continue
			}
			if slices.Contains(strictRunLeaves, path) {
				if !ranAsCommand(corpus, path) {
					uncovered = append(uncovered, path)
				}
				continue
			}
			if !containsLeafPath(corpus, path) {
				uncovered = append(uncovered, path)
			}
		}
		cliUncovered = assertExactUncovered(t, "cli leaves", uncovered, knownUncoveredCLI)
	})

	tools, resources, templates := liveSurface(t)

	// listNames/ListToolDetails used to return (nil, nil) when the
	// server's response array was missing or malformed, so "the server
	// advertised zero tools" and "the response never parsed" looked
	// identical — and the loop below iterates an empty slice and passes
	// vacuously either way. mcpclient.go's parseNamedArray now errors on a
	// malformed response (liveSurface already t.Fatalf's on that), but a
	// genuinely empty registration is itself a real regression this
	// completeness gate exists to catch — an explicit floor makes that case
	// loud too, independent of the parse-error path.
	if len(tools) == 0 {
		t.Fatal("the live MCP server advertised zero tools — either a real regression or a parsing failure masquerading as one; completeness cannot be checked against an empty surface")
	}
	if len(resources) == 0 {
		t.Fatal("the live MCP server advertised zero resources — either a real regression or a parsing failure masquerading as one; completeness cannot be checked against an empty surface")
	}

	t.Run("mcp tools", func(t *testing.T) {
		var uncovered []string
		for _, name := range tools {
			if reason, ok := excludedTools[name]; ok {
				t.Logf("excluded tool: %s — %s", name, reason)
				continue
			}
			if !ranAsTool(corpus, name) {
				uncovered = append(uncovered, name)
			}
		}
		toolsUncovered = assertExactUncovered(t, "mcp tools", uncovered, knownUncoveredTools)
	})

	// Everything above measures the STANDALONE `ctxloom mcp
	// serve` surface (liveSurface spawns it via env.StartMCP()). That is
	// NOT the surface ctxloom documents: scripts/gendocs/main.go's
	// mcpIntro states outright that the standalone surface is a reduced
	// agent-delegation surface with different schemas, and points its
	// generated reference page at cli.NewDocMCPServer() instead -- the
	// RUNNER-terminated surface every harness actually sees through
	// `ctxloom run`/`ctxloom acp serve`. Before this subtest, the tools that
	// exist ONLY on that documented surface (roster, agent_report,
	// agent_fetch_artifact -- named explicitly in mcpIntro's own caution
	// block) had ZERO completeness coverage: they never appeared in
	// `tools` above, so the loop just never saw them, and the gate
	// stayed green regardless of whether a real scenario ever touched
	// them.
	t.Run("mcp tools (documented runner surface)", func(t *testing.T) {
		docTools, err := cli.ListDocMCPToolNames(t.Context())
		if err != nil {
			t.Fatalf("list documented MCP tools: %v", err)
		}
		standalone := make(map[string]bool, len(tools))
		for _, name := range tools {
			standalone[name] = true
		}

		var uncovered []string
		for _, name := range docTools {
			if standalone[name] {
				// Covered (or allowlisted) by the standalone subtest
				// above -- this subtest exists for the DELTA, the
				// runner-only tools the standalone enumeration cannot
				// see at all.
				continue
			}
			if reason, ok := excludedTools[name]; ok {
				t.Logf("excluded runner-only tool: %s — %s", name, reason)
				continue
			}
			if !ranAsTool(corpus, name) {
				uncovered = append(uncovered, name)
			}
		}
		runnerOnlyUncovered = assertExactUncovered(t, "mcp tools (runner-only)", uncovered, knownUncoveredRunnerOnlyTools)
	})

	// One prominent, always-printed deficit statement — the per-subtest
	// t.Logf lines above are detail for someone chasing a specific gap, not
	// a substitute for a reader seeing the size of the debt at a glance.
	//
	// This is fmt.Fprintf(os.Stderr, ...), NOT t.Logf: t.Log/t.Logf output is
	// buffered by the testing package and only flushed for a FAILING test or
	// under -v — on a normal passing `go test` (no -v; this is how `just
	// test-pkg` and CI's plain invocations run), the line never reaches the
	// terminal at all, silently recreating exactly the "clean pass while 43
	// surfaces go unexercised" problem this gate exists to fix. Writing
	// straight to the process's real stderr bypasses that buffering
	// entirely, so a green run cannot look indistinguishable from a run with
	// zero coverage gaps.
	totalUncovered := cliUncovered + toolsUncovered + runnerOnlyUncovered
	fmt.Fprintf(os.Stderr, "COMPLETENESS DEFICIT: %d leaf/tool surfaces are allowlisted UNCOVERED — "+
		"%d CLI leaves, %d MCP tools, %d runner-only MCP tools. This is accepted "+
		"debt (see backfill-task comments above each list), not a clean pass.\n",
		totalUncovered, cliUncovered, toolsUncovered, runnerOnlyUncovered)
	if totalUncovered > maxKnownUncoveredTotal {
		t.Errorf("COMPLETENESS DEFICIT ratchet: %d allowlisted-uncovered surfaces "+
			"exceeds the recorded ceiling of %d (maxKnownUncoveredTotal) — either "+
			"back-fill coverage, or if this growth is deliberate, bump "+
			"maxKnownUncoveredTotal in this same commit and say why",
			totalUncovered, maxKnownUncoveredTotal)
	}

	t.Run("mcp resources", func(t *testing.T) {
		for _, uri := range resources {
			if !strings.Contains(corpus, uri) {
				t.Errorf("uncovered MCP resource: %q", uri)
			}
		}
		for _, tmpl := range templates {
			if reason, ok := excludedTemplates[tmpl]; ok {
				t.Logf("excluded template: %s — %s", tmpl, reason)
				continue
			}
			prefix := tmpl
			if i := strings.IndexByte(tmpl, '{'); i >= 0 {
				prefix = tmpl[:i]
			}
			if !strings.Contains(corpus, prefix) {
				t.Errorf("uncovered MCP resource template: %q (no URI under %q)", tmpl, prefix)
			}
		}
	})
}

// leafCommands returns the "ctxloom ..." command path of every runnable,
// non-hidden leaf in the tree.
func leafCommands(root *cobra.Command) []string {
	var out []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		children := c.Commands()
		hasVisibleChild := false
		for _, ch := range children {
			if !ch.Hidden && ch.Name() != "help" {
				hasVisibleChild = true
			}
			walk(ch)
		}
		if !hasVisibleChild && c.Runnable() && !c.Hidden && c != root {
			out = append(out, c.CommandPath())
		}
	}
	walk(root)
	sort.Strings(out)
	return out
}

// liveSurface starts an MCP server against a minimal project and returns the
// registered tools, resources, and resource templates.
func liveSurface(t *testing.T) (tools, resources, templates []string) {
	t.Helper()
	env, err := testenv.NewTestEnvironment()
	if err != nil {
		t.Fatalf("test env: %v", err)
	}
	// Cleanup's error used to be discarded here too.
	t.Cleanup(func() {
		if err := env.Cleanup(); err != nil {
			t.Errorf("test environment cleanup: %v", err)
		}
	})
	if err := env.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := env.InitGitRepo(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := env.WriteFile(".ctxloom/config.yaml", minimalConfig); err != nil {
		t.Fatalf("write config: %v", err)
	}
	client, err := env.StartMCP()
	if err != nil {
		t.Fatalf("start mcp: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if tools, err = client.ListTools(); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if resources, err = client.ListResources(); err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if templates, err = client.ListResourceTemplates(); err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	return tools, resources, templates
}

// loadCorpus concatenates every feature file and step-definition source so a
// command/tool/resource exercised through a friendly step wrapper still counts
// as covered.
func loadCorpus(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	add := func(glob string) {
		matches, _ := filepath.Glob(glob)
		for _, m := range matches {
			data, err := os.ReadFile(m)
			if err != nil {
				t.Fatalf("read %s: %v", m, err)
			}
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	add("features/*.feature")
	add("steps_*.go")
	return b.String()
}
