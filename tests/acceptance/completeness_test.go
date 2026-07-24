//go:build acceptance

package acceptance

import (
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
	"ctxloom manage install":     {"claude-code", "codex", "kiro", "antigravity"},
	"ctxloom manage config init": {"claude-code", "codex", "kiro", "antigravity"},
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
	// Publisher signing surface: zero acceptance scenarios sign a bundle or
	// list the allowed_signers store. Backfill: task outer-water.
	// "ctxloom signer add"/"ctxloom signer remove" were here too, but J3
	// (steps_j3.go, j3_corporate_signed.feature) now legitimately drives both:
	// the Background's "Alice trusts the company key" step runs `ctxloom
	// signer add ... --project`, and scenario 6's "Alice revokes her trust in
	// the company key" runs `ctxloom signer remove ... --project` — pruned.
	// "ctxloom signer show" was here too, but J7 (steps_j7.go,
	// j7_incident.feature)'s irrevocable-embedded-key scenario now legitimately
	// drives it (before/after the removal attempt) — pruned.
	"ctxloom sign",
	"ctxloom signer list",
	// Relocates an authored bundle to another remote/project; needs a second
	// project/remote fixture beyond this suite's single seeded remote.
	// Backfill: task cheap-pug.
	"ctxloom bundle move",
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
	// Post-reorg coverage debt. The noun-verb CLI reorg (2710ff0) added
	// canonical `trust …`, `mcp …`, `config …`, `container tooling`, `bundle
	// sign`, and `init prompt` leaves ALONGSIDE the retained legacy aliases
	// (`signer …`, `manage config …`, `tooling`, `sign` — still present and
	// still exercised by the corpus under their old names), and the writer-a
	// session work added `session backfill`/`session query` plus the `acp …`
	// surface. The acceptance corpus still drives the legacy spellings, so
	// these new leaves are genuinely uncovered. Allowlisted pending real
	// acceptance coverage; tracked as one backfill task.
	"ctxloom trust accept",
	"ctxloom trust reject",
	"ctxloom trust signer add",
	"ctxloom trust signer list",
	"ctxloom trust signer remove",
	"ctxloom trust signer show",
	"ctxloom mcp register",
	"ctxloom mcp unregister",
	"ctxloom mcp server add",
	"ctxloom mcp server list",
	"ctxloom mcp server remove",
	"ctxloom mcp server show",
	"ctxloom config get",
	"ctxloom config init",
	"ctxloom config show",
	"ctxloom container tooling",
	"ctxloom bundle sign",
	"ctxloom init prompt",
	"ctxloom session backfill",
	"ctxloom session query",
	"ctxloom acp client",
	"ctxloom acp entries",
	"ctxloom acp server",
	// Engine-matrix variants: only the claude-code path is exercised;
	// codex/kiro/antigravity need their own fixtures. Backfill: task glad-skid.
	"ctxloom manage install --engine codex",
	"ctxloom manage install --engine kiro",
	"ctxloom manage install --engine antigravity",
	"ctxloom manage config init --engine codex",
	"ctxloom manage config init --engine kiro",
	"ctxloom manage config init --engine antigravity",
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
	// agent_stop and trigger evaluation remain unexercised. Backfill: task
	// spry-niece.
	"agent_stop",
	"evaluate_triggers",
	// Session-memory tools named only in mcp_tools.feature's prose, never
	// actually invoked (see ranAsTool's doc comment). Backfill: task spry-niece.
	"compact_session",
	"get_previous_session",
	"list_sessions",
}

// assertExactUncovered fails t if got (the actual uncovered set, need not be
// pre-sorted) differs from want (a knownUncovered* allowlist) in either
// direction: an item in got but not want is a NEW gap (something regressed);
// an item in want but not got means the allowlist is stale (it was backfilled
// and should be pruned). label names the surface for the failure message.
func assertExactUncovered(t *testing.T, label string, got, want []string) {
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
}

// excludedLeaves are public-surface entry points deliberately not exercised by a
// scenario, each with the reason. Printed on every run so the exclusion is never
// silent (see plan §7.5 / "no silent caps").
var excludedLeaves = map[string]string{
	"ctxloom manage config edit": "opens $EDITOR on config.yaml; TTY-only, no hermetic fixture",
	"ctxloom config edit":        "noun-verb alias of `manage config edit`; opens $EDITOR on config.yaml, TTY-only, no hermetic fixture",
	"ctxloom mcp serve":          "the MCP server itself; exercised by every @mcp scenario",
	"ctxloom llm serve":          "internal gRPC plugin server",
	"ctxloom remote discover":    "network discovery search; no deterministic fixture (excluded)",
	"ctxloom bundle mcp edit":    "edits an MCP server embedded in a bundle; requires a bundle-embedded MCP fixture (niche)",
	// Remote ops needing richer state than a single-commit fixture provides. The
	// core clone/fetch/install/sync path is covered hermetically by the @remote
	// content scenarios (browse/install/sync/lock against a seeded file:// repo).
	"ctxloom remote update":         "checks installed bundles for a newer SHA; needs a second remote commit. Core fetch covered by @remote scenarios",
	"ctxloom remote upgrade":        "upgrades installed bundles to latest; needs an update cycle. Core fetch covered by @remote scenarios",
	"ctxloom weave":                 "fans a task out across parallel LLM sessions, then an LLM synthesis pass over the results; @live-only, no hermetic fixture",
	"ctxloom session distill":       "@live + requires a real backend session transcript; the fragment/command/bundle distill paths cover the distiller",
	"ctxloom completion zsh":        "shell variant; the completion path is covered via bash",
	"ctxloom completion fish":       "shell variant; the completion path is covered via bash",
	"ctxloom completion powershell": "shell variant; the completion path is covered via bash",
	"ctxloom help":                  "cobra-generated help command",
	"ctxloom memory list":           "reads external backend session history; not reproducible hermetically",
	"ctxloom memory show":           "reads external backend session history; not reproducible hermetically",
	"ctxloom memory compact":        "@live + external backend session history",
	"ctxloom bundle push":           "publishes to a remote forge (push/PR); requires a writable remote, out of hermetic scope",
	"ctxloom fragment push":         "publishes to a remote forge (push/PR); requires a writable remote, out of hermetic scope",
	"ctxloom command push":          "publishes to a remote forge (push/PR); requires a writable remote, out of hermetic scope",
	"ctxloom profile push":          "publishes to a remote forge (push/PR); requires a writable remote, out of hermetic scope",
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
var excludedTools = map[string]string{
	"recover_session": "resolves the most-recent backend session; requires a real backend transcript",
}

// excludedTemplates are resource templates with no hermetic scenario. Currently
// none — every templated resource is exercised, including remotes/{name}/contents
// against a seeded file:// remote.
var excludedTemplates = map[string]string{}

// TestCompleteness enforces that every public CLI leaf, MCP tool, and MCP
// resource is exercised by some scenario or step, or is explicitly excluded.
func TestCompleteness(t *testing.T) {
	corpus := loadCorpus(t)

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
		assertExactUncovered(t, "cli leaves", uncovered, knownUncoveredCLI)
	})

	tools, resources, templates := liveSurface(t)

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
		assertExactUncovered(t, "mcp tools", uncovered, knownUncoveredTools)
	})

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
	t.Cleanup(func() { _ = env.Cleanup() })
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
