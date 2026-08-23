//go:build acceptance

package acceptance

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/mcp"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// ranAsTool reports whether an MCP tool was actually invoked by a scenario (a
// genuine `the agent calls tool "<name>"` step — see steps_mcp.go), as
// opposed to merely being named somewhere in the corpus. Plain substring
// matching over tool names is the same vacuous-coverage hole ranAsCommand
// closes for CLI leaves — a feature's own prose naming a tool as explicitly
// NOT covered satisfies it just as well as an invocation does. Every real
// invocation goes through callTool via one of the two `calls tool "<name>"`
// step patterns, so that literal substring is the exact signal.
func ranAsTool(corpus, name string) bool {
	return strings.Contains(corpus, `calls tool "`+name+`"`)
}

// knownUncoveredTools is knownUncoveredCLI's MCP-tool counterpart: the exact
// set of registered tools (from a plain, non-forwarding `mcp serve`) this
// gate accepts as uncovered, checked for exact-set equality the same way.
var knownUncoveredTools = []string{
	// Agent-delegation bus + trigger evaluation: agent_run is exercised by J002100
	// (steps_j002100_delegation.go, j002100_delegation.feature — a coordinator spawning
	// delegated children and auditing their journaled privilege grant).
	// agent_send/agent_recv are now exercised by J002300
	// (steps_j002300_cross_engine_delegation.go, j002300_cross_engine_delegation.feature
	// — the real two-way bus, both directions, content asserted on each side).
	// agent_stop is now exercised by J002100's failure-path scenario: idempotent
	// double-stop plus a refused stop on a run id that was never spawned (a
	// real regression this scenario now pins).
	//
	// evaluate_triggers LEFT this list when mcp_tools.feature gained a scenario
	// that seeds a Deferred task, cans the model's verdict around the harp
	// taskloom mints for it, and asserts the verdict is ATTRIBUTED to that
	// task — the result's counters cannot distinguish a real answer from a
	// silently dropped one, which is exactly why the tool carries an `omitted`
	// field. Writing it found runTriageCall omitting the label's env entirely.
	//
	// compact_session, get_previous_session and list_sessions are absent
	// because mcp_tools.feature actually INVOKES them — being named in a
	// feature's prose is not coverage, which is the hole ranAsTool closes.
	// Each is asserted on a marker that exists only in its own fixture's
	// transcript, so a well-formed report of nothing cannot pass.
}

// knownUncoveredRunnerOnlyTools is the exact set of tools that exist ONLY on
// the documented (runner-terminated, mcp.NewDocMCPServer) MCP surface -- not
// on the standalone `ctxloom mcp serve` surface knownUncoveredTools governs
// -- and are not yet exercised by any scenario. roster, agent_report and
// agent_fetch_artifact are named in scripts/gendocs/main.go's mcpIntro as
// existing only on this surface, which is exactly why they need a list of
// their own: the standalone-surface census cannot see them at all, so without
// this list they are invisible rather than red. Backfill still needed.
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

// excludedTools are registered MCP tools no hermetic scenario reaches.
//
// It is EMPTY, and keeping it empty is the standard: an entry here silences a
// real gap, so it earns its place only with a reason that survives checking.
// "Exercised at another layer" must name the test that does it; "requires a
// real backend" must be true — a mock backend plus a directly seeded canonical
// transcript reaches more of this surface hermetically than it first appears
// (see steps_recover_session.go). A tool nothing drives belongs RED below,
// where it is visible, not excluded here where it is not.
var excludedTools = map[string]string{}

// excludedTemplates are resource templates with no hermetic scenario. Currently
// none — every templated resource is exercised, including remotes/{name}/contents
// against a seeded file:// remote.
var excludedTemplates = map[string]string{}

// maxKnownUncoveredTotal is a RATCHET on the combined size of every
// knownUncovered* allowlist (currently 0 MCP tools + 3 runner-only MCP tools
// = 3). CLI leaves are NOT counted here: they are decided from real coverage
// data by TestCLICoverage_EveryLeafActuallyRan in the test-acceptance-cover
// lane, which reads block counters rather than this corpus.
//
// It exists because assertExactUncovered's
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
//
// RAISING this number is the one direction that normally must not happen, so
// a raise carries its reason. A raise is legitimate when the MEASUREMENT gets
// stricter rather than the coverage getting worse — tightening loadCorpus to
// count only scenarios that actually RUN exposes leaves whose sole coverage is
// @wip or @live, which were never covered in the first place. Recording
// them as tracked debt rather than parking them in excludedLeaves is the point,
// because excludedLeaves is skipped BEFORE counting and would have hidden them
// from this ratchet entirely.
//
// Decided with the human (set it and ratchet): take the true number now and
// burn it down from here. Do not raise it again to accommodate a real
// regression — that number was a floor to work off, not a budget to spend.
//
// LOWERED from 24 to 21 by content_distill.feature, which retired exactly the
// three leaves that raise had exposed (`bundle`, `command` and `fragment
// distill`). Lowering it in the same change is the mechanism working as
// intended: the ratchet only tracks the debt down if closing gaps costs the
// allowlist its slack.
//
// LOWERED again, 21 to 17, by session_hooks.feature retiring the four hidden
// hook callbacks, then 17 to 14 by mcp_tools.feature retiring compact_session,
// get_previous_session and list_sessions, then 14 to 10 by manage.feature
// retiring the codex and kiro engine-matrix rows, then 10 to 9 by mcp.feature
// retiring `mcp server edit`, then 9 to 8 by mcp_tools.feature retiring
// evaluate_triggers — the last uncovered MCP tool.
// LOWERED 8 -> 3: CLI leaves no longer count here at all. Their completeness
// is decided from real coverage data by TestCLICoverage_EveryLeafActuallyRan,
// which has its own exemption list and its own fails-in-both-directions
// contract. What remains under this ratchet is the MCP surface alone.
const maxKnownUncoveredTotal = 3

// TestCompleteness enforces that every public CLI leaf, MCP tool, and MCP
// resource is exercised by some scenario or step, or is explicitly excluded.
func TestCompleteness(t *testing.T) {
	corpus := loadCorpus(t)

	// Populated by the three allowlisted subtests below, then folded into one
	// prominent deficit statement (and ratcheted) once they've all run.
	var toolsUncovered, runnerOnlyUncovered int

	tools, resources, templates := liveSurface(t)

	// "The server advertised zero tools" and "the response never parsed" must
	// not look identical: the loop below iterates an empty slice and passes
	// vacuously either way. mcpclient.go's parseNamedArray errors on a
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
	// generated reference page at mcp.NewDocMCPServer() instead -- the
	// RUNNER-terminated surface every harness actually sees through
	// `ctxloom run`/`ctxloom acp serve`. Before this subtest, the tools that
	// exist ONLY on that documented surface (roster, agent_report,
	// agent_fetch_artifact -- named explicitly in mcpIntro's own caution
	// block) had ZERO completeness coverage: they never appeared in
	// `tools` above, so the loop just never saw them, and the gate
	// stayed green regardless of whether a real scenario ever touched
	// them.
	t.Run("mcp tools (documented runner surface)", func(t *testing.T) {
		docTools, err := mcp.ListDocMCPToolNames(t.Context())
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
	totalUncovered := toolsUncovered + runnerOnlyUncovered
	fmt.Fprintf(os.Stderr, "COMPLETENESS DEFICIT: %d MCP surfaces are allowlisted UNCOVERED — "+
		"%d MCP tools, %d runner-only MCP tools. This is accepted debt (see "+
		"backfill-task comments above each list), not a clean pass. CLI LEAF "+
		"coverage is no longer counted here: it is decided from real coverage "+
		"data by TestCLICoverage_EveryLeafActuallyRan in the "+
		"`just test-acceptance-cover` lane.\n",
		totalUncovered, toolsUncovered, runnerOnlyUncovered)
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

// liveSurface starts an MCP server against a minimal project and returns the
// registered tools, resources, and resource templates.
func liveSurface(t *testing.T) (tools, resources, templates []string) {
	t.Helper()
	env, err := testenv.NewTestEnvironment()
	if err != nil {
		t.Fatalf("test env: %v", err)
	}
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
	if err := writeMinimalConfig(env); err != nil {
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

// loadCorpus concatenates step-definition source plus the feature-file text of
// scenarios that WOULD ACTUALLY RUN, so a command exercised through a friendly
// step wrapper still counts as covered — but one named only by a scenario that
// never executes does not.
//
// The tag filter is the whole point. Concatenating every feature file verbatim
// would credit a @wip scenario's command as covered while acceptance_test.go's
// default expression excludes it from every run — and @wip IS the tag for
// "cannot be honestly greened yet", so the gate would be most generous exactly
// where coverage is most absent. A @wip row can even drive flags that do not
// exist on the command and still grant credit.
func loadCorpus(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	// RECURSIVE, deliberately: features/ is being split into journeys/ and
	// cli/ subdirectories. A flat "features/*.feature" glob silently stops
	// seeing a file the moment it moves into one — and this gate reads the
	// corpus to decide what is COVERED, so a blind glob does not fail loudly,
	// it reports the whole CLI surface as uncovered (or, with an allowlist
	// that happens to match, reports nothing at all). Walk instead.
	for _, m := range featureFilesOrFail(t, "features") {
		b.WriteString(runnableFeatureText(t, m))
		b.WriteByte('\n')
	}
	for _, m := range globOrFail(t, "steps_*.go") {
		b.Write(readOrFail(t, m))
		b.WriteByte('\n')
	}
	return b.String()
}

// excludedCorpusTags mirrors acceptance_test.go's default tag expression. A
// scenario carrying any of these does not run by default, so its text must not
// grant coverage. Keep in step with that expression: a tag excluded there and
// missing here silently re-opens this hole.
var excludedCorpusTags = []string{"@wip", "@live", "@future", "@network", "@container"}

// runnableFeatureText returns the feature's text with every non-running
// scenario body removed. Tags are inherited: a tag line above `Feature:`
// excludes the whole file, exactly as godog treats it.
//
// The parse is deliberately line-based rather than a real Gherkin parse. It
// only has to answer "would this block run", and a dependency on a parser here
// would be a second, drifting model of the tag rules the suite already owns.
func runnableFeatureText(t *testing.T, path string) string {
	t.Helper()
	lines := strings.Split(string(readOrFail(t, path)), "\n")

	var out []string
	var pending []string // tag lines seen since the last block
	skipping := false
	featureExcluded := false
	seenFeature := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "@") {
			pending = append(pending, trimmed)
			out = append(out, line) // tags themselves never name a command
			continue
		}

		isFeature := strings.HasPrefix(trimmed, "Feature:")
		isBlock := strings.HasPrefix(trimmed, "Scenario:") ||
			strings.HasPrefix(trimmed, "Scenario Outline:") ||
			strings.HasPrefix(trimmed, "Background:")

		switch {
		case isFeature:
			seenFeature = true
			featureExcluded = anyExcludedTag(pending)
			pending = nil
			skipping = featureExcluded
		case isBlock:
			skipping = featureExcluded || anyExcludedTag(pending)
			pending = nil
		}
		_ = seenFeature

		if !skipping {
			out = append(out, line)
		}
	}
	return expandOutlineRows(strings.Join(out, "\n"))
}

// expandOutlineRows appends, for every Examples row in the text, a copy of the
// preceding step lines with each <placeholder> substituted by that row's value.
//
// WHY THIS EXISTS. This gate credits a leaf on literal command text, and an
// Examples table never produces that text — a step reads `--engine <engine>`,
// while the census is looking for `--engine codex`. Without expansion, a
// per-engine matrix written as an Outline reports every one of its engines
// uncovered no matter how thoroughly it exercises them, which pushes authors
// into writing N near-identical scenarios to satisfy the gate.
//
// Expanding the rows here lets a scenario be written in whatever shape reads
// best while the census still sees every command the suite will actually
// run.
//
// It is deliberately TEXTUAL and appended rather than substituted in place:
// this function feeds a substring census, not a parser, so a sloppy expansion
// can only ADD text that a real row really produces — it can never hide a
// command that was already visible.
func expandOutlineRows(text string) string {
	lines := strings.Split(text, "\n")
	var out strings.Builder
	out.WriteString(text)

	var steps []string  // step lines seen since the last Examples block
	var header []string // column names of the Examples table being read

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Scenario Outline:"):
			steps, header = nil, nil
		case strings.HasPrefix(trimmed, "Scenario:"), strings.HasPrefix(trimmed, "Feature:"):
			steps, header = nil, nil
		case strings.HasPrefix(trimmed, "Examples:"):
			header = nil
		case strings.HasPrefix(trimmed, "|"):
			cells := splitRow(trimmed)
			if header == nil {
				header = cells
				continue
			}
			for _, s := range steps {
				expanded := s
				for i, col := range header {
					if i < len(cells) {
						expanded = strings.ReplaceAll(expanded, "<"+col+">", cells[i])
					}
				}
				out.WriteString("\n")
				out.WriteString(expanded)
			}
		case strings.Contains(trimmed, "<"):
			// A placeholder-bearing step of the Outline currently being read.
			steps = append(steps, line)
		}
	}
	return out.String()
}

// splitRow splits a Gherkin table row into trimmed cell values.
func splitRow(row string) []string {
	parts := strings.Split(strings.Trim(row, "|"), "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
}

func anyExcludedTag(tagLines []string) bool {
	for _, l := range tagLines {
		for _, field := range strings.Fields(l) {
			for _, ex := range excludedCorpusTags {
				if field == ex || strings.HasPrefix(field, ex+".") {
					return true
				}
			}
		}
	}
	return false
}

// featureFilesOrFail walks dir recursively and returns every .feature file,
// sorted. Replaces a flat glob so the journeys/ + cli/ split cannot make this
// gate blind — see loadCorpus. Sorted so the corpus text is stable run to run.
func featureFilesOrFail(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".feature") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if len(found) == 0 {
		t.Fatalf("walk %s: no .feature files found — this gate reads the corpus to "+
			"decide coverage, so an empty result is a broken walk, not an empty suite", dir)
	}
	sort.Strings(found)
	return found
}

func globOrFail(t *testing.T, glob string) []string {
	t.Helper()
	matches, err := filepath.Glob(glob)
	if err != nil {
		t.Fatalf("glob %s: %v", glob, err)
	}
	return matches
}

func readOrFail(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a test-owned path from a fixed glob
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
