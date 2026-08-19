// Untagged, like the code it checks: P1's verdict, its config renderer and its
// registry↔feature agreement all run under plain `just test` /
// `just test-pkg ./tests/acceptance/`, with no engine installed and no paid
// turn spent.
//
// WHY THESE ARE THE POINT. Every P1 cell is @live, so nothing in the default
// suite ever executes approachAssert against a real run. Its whole value is that
// it refuses two specific lies:
//
//  1. THE DEGRADED RUN. ctxloom treats a delivery preference it cannot honour as
//     a warning and launches anyway. Such a run answers the question perfectly
//     and proves nothing about the approach in the cell's own name. If
//     approachPinHonoured is ever weakened or deleted, every cell in this probe
//     goes green and the probe becomes a duplicate of P0 — silently, which is
//     the only way this suite can actually fail.
//
//  2. THE PIN THAT WAS NEVER WRITTEN. `surfaces:` is an optional map key. Misspell
//     it, indent it wrong, or attach it to the wrong node and the config still
//     loads, the agent still launches, the nonce still arrives — and the cell
//     claims a mechanism nobody selected. So the renderer is parsed here by the
//     REAL agent parser, not eyeballed.
//
// And one piece of bookkeeping that is not a lie so much as a slow leak: the
// registry says which (engine, approach) pairs exist and the feature says which
// ones RUN. The shared drift gate in capability_probe_registry_test.go compares
// them at engine/runtime/workspace granularity, which cannot see P1's variants —
// two of these cells share an engine and both axes. The variant-level agreement
// is checked below, in both directions, so a cell added to one file and not the
// other is a hermetic red rather than a quiet hole.
package acceptance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gherkin "github.com/cucumber/gherkin/go/v26"
	messages "github.com/cucumber/messages/go/v21"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/schema"
)

// approachCellFixture is a green P1 run, used as the baseline every check below
// mutates one thing about.
func approachCellFixture() *approachState {
	return &approachState{
		engine:    "claude-code",
		runtime:   "host",
		workspace: "none",
		variant:   "system-prompt",
		approach:  "system-prompt",
		nonce:     "swift-amber-falcon",
		stdout:    "{\"hello\":\"swift-amber-falcon\"}\n",
		stderr:    "ctxloom: session started\n",
	}
}

// TestApproachAssert_AcceptsTheOneShapeItAsksFor. The positive control: if this
// ever fails, every negative check below is proving nothing about a passable
// baseline.
func TestApproachAssert_AcceptsTheOneShapeItAsksFor(t *testing.T) {
	require.NoError(t, approachAssert(approachCellFixture()))
}

// TestApproachAssert_RefusesARunThatDegradedOffThePin is THE test of this
// probe. Each marker is a real path by which production announces it is using
// the engine's default delivery instead of the pinned one — and in every case
// the run is otherwise PERFECT: exit 0, exact JSON, correct nonce. A verdict
// that looked only at stdout would report green and would be describing P0.
func TestApproachAssert_RefusesARunThatDegradedOffThePin(t *testing.T) {
	require.NotEmpty(t, approachDegradeMarkers,
		"an empty marker list would make every case below vacuous — the probe would accept any degraded run")

	// The real warning lines production emits, so the substrings are matched
	// against the shape they actually appear in rather than against themselves.
	lines := map[string]string{
		"default delivery":                         "ctxloom: warning: agent \"hello\": surfaces context=hook: claude-code does not support it (supports: unsafe-file) — using claude-code's default delivery\n",
		"launching without managed hooks/commands": "ctxloom: warning: config load failed; launching without managed hooks/commands: open .ctxloom/config.yaml: no such file or directory\n",
	}

	for _, m := range approachDegradeMarkers {
		t.Run(m.Marker, func(t *testing.T) {
			line, ok := lines[m.Marker]
			require.True(t, ok,
				"approachDegradeMarkers gained %q with no sample warning here. Add the line production actually prints: a marker matched only against itself does not prove it would fire on the real thing.", m.Marker)
			require.Contains(t, line, m.Marker, "the sample line must contain the marker, or this case tests nothing")
			require.NotEmpty(t, m.Why, "a degrade marker with no explanation reds a cell without telling anyone why")

			s := approachCellFixture()
			s.stderr = line
			err := approachAssert(s)
			require.Error(t, err,
				"a run that DEGRADED off the pinned approach answered the question through a channel this cell did not select. Reporting it green makes P1 an expensive duplicate of P0.")

			shape, ok := probeShapeOf(err)
			require.True(t, ok, "the verdict must carry its shape as data, not only as prose")
			require.Equal(t, channelComposedContext.Shape, shape,
				"a degraded pin is a CONTEXT-DELIVERY failure — the context arrived through the wrong channel — and must not be reported as some other subsystem's fault")
			require.Contains(t, err.Error(), m.Marker,
				"the failure must quote the marker it matched, or nobody can tell which degrade path fired")
			require.Contains(t, err.Error(), "system-prompt",
				"the failure must name the approach that was pinned and not honoured")
		})
	}
}

// codexHookNotWrittenWarning is the VERBATIM stderr line that made P1's first
// codex finding false. Kept exactly as production emitted it (captured
// from the codex hook cell's own run) so the guard is matched against
// the real thing rather than against a paraphrase of it.
const codexHookNotWrittenWarning = "ctxloom: warning: codex hooks and MCP servers were NOT written: codex settings/prompts/skills are delivered per-session at launch; no durable project home exists — see config_home. They are delivered into this session's own CODEX_HOME when an agent whose binding declares `config_home: project` launches; there is no durable project file to materialize. codex's cwd-keyed AGENTS.md context is unaffected and was still written.\n"

// TestApproachAssert_RefusesAHookCellWhoseHookWasNeverWritten is the regression
// test for a false FINDING, which is a rarer and more expensive thing than a
// false test.
//
// The original codex hook cell went green, and P1 recorded on that basis that
// the fragment-drop finding was fixed. It was not: the run's stderr
// said the hook surface had not been written at all, so the nonce had arrived
// through codex's natively-read AGENTS.md and the cell's subject — the hook —
// had never existed in that session. stdout looked identical either way, which
// is precisely why the check has to live off stdout.
func TestApproachAssert_RefusesAHookCellWhoseHookWasNeverWritten(t *testing.T) {
	s := approachCellFixture()
	s.engine, s.variant, s.approach = "codex", "hook", "hook"
	s.stdout = "{\"hello\":\"swift-amber-falcon\"}\n"
	s.stderr = codexHookNotWrittenWarning

	err := approachAssert(s)
	require.Error(t, err,
		"a hook-pinned cell whose hook was never written must not pass. It did once, and the green became a written finding that another slice had to come back and overturn.")
	shape, ok := probeShapeOf(err)
	require.True(t, ok)
	require.Equal(t, channelComposedContext.Shape, shape,
		"an uninstalled hook is a CONTEXT-DELIVERY failure: the context arrived, but not by the channel this cell names")
	require.Contains(t, err.Error(), "NOT written",
		"the failure must quote production's own words, so the next reader can find the same line in their own run")
}

// TestApproachRequiredSurfaceDelivered_IsScopedToTheApproachThatRidesTheHook.
// The same launch legitimately reports other undelivered surfaces, and a red a
// context probe cannot act on is its own kind of noise. Only the approach that
// is COUPLED to the settings surface (agent.ApproachHook's own doc) cares.
func TestApproachRequiredSurfaceDelivered_IsScopedToTheApproachThatRidesTheHook(t *testing.T) {
	for _, approach := range []string{"unsafe-file", "system-prompt"} {
		t.Run(approach, func(t *testing.T) {
			s := approachCellFixture()
			s.engine, s.variant, s.approach = "codex", approach, approach
			s.stderr = codexHookNotWrittenWarning
			require.NoError(t, approachAssert(s),
				"%s carries itself and does not ride the hook surface; reddening it for an unwritten hook would be a red nobody can act on", approach)
		})
	}

	t.Run("an unrelated NOT-written line does not red a hook cell", func(t *testing.T) {
		s := approachCellFixture()
		s.engine, s.variant, s.approach = "codex", "hook", "hook"
		s.stderr = "ctxloom: warning: codex slash-command prompts were NOT written: no durable project home exists\n"
		require.NoError(t, approachAssert(s),
			"the guard must require BOTH the not-written signal and the hook on the same line: undelivered PROMPTS say nothing about the context channel")
	})
}

// TestApproachAssert_RunFailureBeatsThePinCheck keeps the diagnostic ORDER
// honest. A crashed run's stderr can contain almost anything; if the pin check
// ran first, an engine that died while printing an unrelated warning would be
// reported as a delivery-channel problem and the real failure would be lost.
func TestApproachAssert_RunFailureBeatsThePinCheck(t *testing.T) {
	s := approachCellFixture()
	s.runErr = errors.New("exit status 1")
	s.exitCode = 1
	s.stderr = "ctxloom: warning: config load failed; launching without managed hooks/commands: boom\n"

	err := approachAssert(s)
	require.Error(t, err)
	shape, ok := probeShapeOf(err)
	require.True(t, ok)
	require.Equal(t, shapeRunFailed, shape,
		"a run that did not complete is a RUN failure. Attributing it to the delivery channel would send the next person to the wrong subsystem.")
}

// TestApproachAssert_KeepsP0sStrictness. P1 varies the mechanism and NOTHING
// else — least of all the output contract. The cheapest way to "fix" a red cell
// here would be to accept a fenced or prefaced answer, so the shapes that must
// stay red are pinned by name.
func TestApproachAssert_KeepsP0sStrictness(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*approachState)
		want   probeShape
	}{
		{
			name:   "silent no-op: exit 0 and nothing on stdout",
			mutate: func(s *approachState) { s.stdout = "" },
			want:   shapeSilentNoOp,
		},
		{
			name: "fenced JSON is a FORMAT failure, never a tolerated shape",
			mutate: func(s *approachState) {
				s.stdout = "```json\n{\"hello\":\"swift-amber-falcon\"}\n```"
			},
			want: shapeOutputFormat,
		},
		{
			name: "a prose preamble is a FORMAT failure",
			mutate: func(s *approachState) {
				s.stdout = "Here is your JSON:\n{\"hello\":\"swift-amber-falcon\"}"
			},
			want: shapeOutputFormat,
		},
		{
			name:   "well-formed JSON without the nonce is a CONTEXT-DELIVERY failure",
			mutate: func(s *approachState) { s.stdout = "{\"hello\":\"world\"}" },
			want:   channelComposedContext.Shape,
		},
		{
			name: "an extra key is a SHAPE failure, not a value one",
			mutate: func(s *approachState) {
				s.stdout = "{\"hello\":\"swift-amber-falcon\",\"note\":\"hi\"}"
			},
			want: shapeShape,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := approachCellFixture()
			tc.mutate(s)
			err := approachAssert(s)
			require.Error(t, err)
			shape, ok := probeShapeOf(err)
			require.True(t, ok)
			require.Equal(t, tc.want, shape)
		})
	}
}

// TestApproachAssert_CellIdentityCarriesTheVariant. Two P1 cells share
// claude-code/host/none. If the cell id dropped the variant they would collide
// in the minted-harp ledger — the second cell would be handed the first one's
// harp, and PX's leak scanner could no longer tell a real cross-cell leak from a
// deliberate collision.
func TestApproachAssert_CellIdentityCarriesTheVariant(t *testing.T) {
	sys := approachCellFixture()
	hook := approachCellFixture()
	hook.variant, hook.approach = "hook", "hook"

	require.NotEqual(t, sys.cell(), hook.cell(),
		"two cells differing only by approach must be different ledger keys, or they share a nonce")
	require.Contains(t, sys.cell().String(), "variant=system-prompt",
		"a failure message must stamp which mechanism it is about")
	require.Equal(t, probeP1, sys.cell().Probe)

	first, err := probeHarps.Mint(sys.cell())
	require.NoError(t, err)
	second, err := probeHarps.Mint(hook.cell())
	require.NoError(t, err)
	require.NotEqual(t, first, second,
		"the ledger must mint a distinct harp per variant; a shared value would make one cell satisfiable by the other's plant")
}

// TestApproachConfigYAML_ActuallyPinsTheApproach parses the fixture's own
// config.yaml with the REAL agent parser. This is the check that a probe about
// config keys cannot do by inspection: `surfaces:` is optional, so a misspelling
// or a wrong indent yields a config that loads clean, an agent that launches, a
// nonce that arrives, and a cell claiming a mechanism nobody selected.
func TestApproachConfigYAML_ActuallyPinsTheApproach(t *testing.T) {
	a := liveAgents["claude"]
	require.NotEmpty(t, a.config, "the live registry's claude row must carry a config, or this parses nothing")

	for _, approach := range []string{"unsafe-file", "system-prompt", "hook"} {
		t.Run(approach, func(t *testing.T) {
			rendered := approachConfigYAML(a, "claude", "host", approach)

			// Round-trip through the parser production uses for an agent
			// binding, on the binding sub-document the config key holds.
			binding := approachBindingYAML(t, rendered)
			parsed, err := agents.ParseAgent([]byte(binding))
			require.NoError(t, err, "the rendered binding must parse as an agent:\n%s", binding)
			require.Equal(t, map[string]string{"context": approach}, parsed.Surfaces,
				"the fixture must actually deliver the pin. An unparsed `surfaces:` key is silent: the run takes the engine's default and the cell claims an approach nobody selected.\n%s", binding)
			require.Equal(t, "bypass", parsed.Permissions,
				"the pin must be ADDED to P0's binding, not replace parts of it — a P1 cell differs from a P0 cell by exactly one block")
		})
	}

	// The container axis composes with the pin rather than displacing it: the
	// two keys sit side by side under the same binding. container-rootless,
	// not the retired undifferentiated "container" spelling (task
	// unwatched-discharge) — config-schema.json's `runtime` enum no longer
	// accepts it, so a fixture still writing it would build a config no real
	// invocation could load.
	binding := approachBindingYAML(t, approachConfigYAML(a, "claude", "container-rootless", "hook"))
	parsed, err := agents.ParseAgent([]byte(binding))
	require.NoError(t, err)
	require.Equal(t, "container-rootless", parsed.Runtime)
	require.Equal(t, map[string]string{"context": "hook"}, parsed.Surfaces)
}

// TestApproachConfigYAML_RuntimeMustBeSchemaValid is the mutation target for
// task unwatched-discharge's fix (P1 wrote the retired undifferentiated
// "runtime: container" — resources/schema/input/config-schema.json's `runtime`
// enum now only accepts host|container-rootless|container-rootful, and an
// unrecognized value degrades a live run to the host under --degraded,
// silently, per isolation.go's warnUnknownAxes).
//
// The test above (TestApproachConfigYAML_ActuallyPinsTheApproach) only proves
// the rendered binding PARSES — agents.ParseAgent is deliberately lenient, it
// accepts any string into Runtime, which is exactly how the retired spelling
// went unnoticed here for as long as it did. This test instead runs the SAME
// rendered bytes through the REAL schema validator production's own config
// loader uses (internal/schema.NewConfigValidator, the seam
// internal/config/unknown_keys.go's classifyValidationError sits on top of),
// so a P1 fixture cannot drift back to a value the schema rejects even if
// ParseAgent stays lenient forever.
//
// PROVEN BY MUTATION (task unwatched-discharge, 2026-08-18): changing the
// runtime argument below from "container-rootless" back to the retired
// "container" turns this test RED — schema validation rejects it with
// `'/agents/hello/runtime' does not validate ... enum: value must be one of
// "host", "container-rootless", "container-rootful"`, reproducing exactly the
// silent degradation this task exists to close. Restoring
// "container-rootless" turns it green again. See the task's own report for
// the transcript of both runs.
//
// NOW USES approachConfigYAML(a, ...) — the actual P1 renderer, not a
// hand-built stand-in. It could not before (task audacious-sandworm,
// 2026-08-18): liveAgents' shared base config (live_engine_registry.go) then
// carried `profiles:\n  defaults: []`, a key config-schema.json's own
// retiredKeys table (internal/config/unknown_keys.go) says was retired
// independently of this task — reusing it here would have made this test red
// for an unrelated, pre-existing reason and hidden the one this test exists
// to catch. That key is gone now (live_engine_registry_test.go's
// TestLiveAgents_ConfigValidatesAgainstSchema gates it directly), so this test
// exercises the SAME bytes a live P1 cell would actually write instead of a
// parallel minimal config that could silently drift from the real renderer.
func TestApproachConfigYAML_RuntimeMustBeSchemaValid(t *testing.T) {
	v, err := schema.NewConfigValidator()
	require.NoError(t, err, "the embedded config schema must compile")

	a := liveAgents["claude"]
	require.NotEmpty(t, a.config, "the live registry's claude row must carry a config, or this validates nothing")

	for _, runtime := range []string{"container-rootless", "container-rootful"} {
		t.Run(runtime, func(t *testing.T) {
			rendered := approachConfigYAML(a, "claude", runtime, "system-prompt")
			require.NoError(t, v.ValidateBytes([]byte(rendered)),
				"a P1 cell's own rendered config must validate against config-schema.json's runtime enum, or a live cell built from it would silently degrade to host instead of exercising a container:\n%s", rendered)
		})
	}
}

// approachBindingYAML lifts the `agents: hello:` sub-document out of a rendered
// config.yaml and de-indents it, so the real agent parser can be pointed at
// exactly the bytes the config key holds.
//
// It FAILS the test when the binding is not there. Returning an empty document
// would make every assertion above pass against nothing, which is the failure
// mode this whole file exists to refuse.
func approachBindingYAML(t *testing.T, config string) string {
	t.Helper()
	const head = "agents:\n  " + matrixAgent + ":\n"
	i := strings.Index(config, head)
	require.GreaterOrEqual(t, i, 0, "the rendered config has no %q binding:\n%s", matrixAgent, config)

	var out []string
	for _, line := range strings.Split(config[i+len(head):], "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		require.True(t, strings.HasPrefix(line, "    "),
			"line %q left the binding block — the renderer's indentation changed and the pin may now attach to a different node", line)
		out = append(out, strings.TrimPrefix(line, "    "))
	}
	require.NotEmpty(t, out, "the binding block is empty")
	return strings.Join(out, "\n") + "\n"
}

// TestApproachPinAcceptedByEngine_AgreesWithTheEnginesOwnTable holds the
// registry to PRODUCTION'S answer, in both directions, with no engine installed.
//
// This is what makes "gated-out" mean something. The design's rule is that a
// probe for a capability an engine DECLARES absent has no Examples row at all —
// and "declares" has to mean the engine's ApproachTable, not somebody's memory
// of it. A wired cell whose engine does not carry the approach would skip or red
// forever; a gated-out cell whose engine DOES carry it is coverage silently
// deleted. Both are red here.
func TestApproachPinAcceptedByEngine_AgreesWithTheEnginesOwnTable(t *testing.T) {
	p, ok := probeSpecByName(probeP1)
	require.True(t, ok)
	require.NotEmpty(t, p.Cells, "an empty cell list would make this check vacuous")

	for _, c := range p.Cells {
		approach := approachOfVariant(t, c.Variant)
		t.Run(c.ID(p.Name).String(), func(t *testing.T) {
			err := approachPinAcceptedByEngine(c.Engine, approach)
			switch c.Status {
			case probeGatedOut:
				require.Error(t, err,
					"%s is recorded gated-out, but %s's ApproachTable DOES declare %q for its context surface. The registry is deleting real coverage: this cell can run.",
					c.ID(p.Name), c.Engine, approach)
				require.Contains(t, c.Reason, "ApproachTable",
					"a gated-out P1 cell must name the table that declares the absence, so the claim is checkable")
			default:
				require.NoError(t, err,
					"%s is wired, but %s does not declare %q. A cell an engine cannot pin is gated-out BY ABSENCE (no Examples row, a written reason), never a red.",
					c.ID(p.Name), c.Engine, approach)
			}
		})
	}
}

// TestApproachVariantNamesItsApproach pins the two-column agreement rule the
// Given step enforces on every cell before it spends a turn.
func TestApproachVariantNamesItsApproach(t *testing.T) {
	require.NoError(t, approachVariantNamesItsApproach("hook", "hook"))
	require.NoError(t, approachVariantNamesItsApproach("unsafe-file-shared", "unsafe-file"),
		"a qualified variant is allowed: the qualifier says WHERE the approach was observed")

	err := approachVariantNamesItsApproach("hook", "system-prompt")
	require.Error(t, err, "a variant naming one mechanism while pinning another makes the tag, the ledger key and the config point at three different things")
	require.Contains(t, err.Error(), "system-prompt")
}

// TestP1_FeatureAndRegistryAgreeOnEveryVariant closes the gap the shared drift
// gate cannot reach.
//
// capability_probe_registry_test.go compares registry rows to Examples rows by
// engine/runtime/workspace. P1's cells are not distinguishable that way — two of
// them are claude-code/host/none — so a variant could be dropped from either
// file with the shared gate still green. Here the comparison is by the full
// TAG SET a cell addresses itself with, in both directions:
//
//   - every runnable registry cell selects EXACTLY ONE Examples block;
//   - every Examples block is claimed by exactly one registry cell;
//   - no gated-out-by-absence cell selects any block at all.
func TestP1_FeatureAndRegistryAgreeOnEveryVariant(t *testing.T) {
	p, ok := probeSpecByName(probeP1)
	require.True(t, ok)
	require.NotEmpty(t, p.Feature, "P1 must name its feature file, or the drift gates skip it entirely")

	blocks := featureExamplesTagSets(t, filepath.Join("features", p.Feature))
	require.NotEmpty(t, blocks, "%s declared no Examples blocks — this check would compare against nothing", p.Feature)

	claimed := make([]int, len(blocks))
	for _, c := range p.Cells {
		want := c.Tags(p.Name)
		var matched []int
		for i, have := range blocks {
			if tagSetContainsAll(have, want) {
				matched = append(matched, i)
			}
		}
		runnable := c.Status != probeGatedOut && c.Status != probeDeferred

		if !runnable {
			require.Empty(t, matched,
				"%s is gated-out/deferred, but %s carries an Examples block selected by its tags. A scenario for a capability the engine declares gone skips forever and reads as coverage.",
				c.ID(p.Name), p.Feature)
			continue
		}
		require.Len(t, matched, 1,
			"%s must be selected by EXACTLY ONE Examples block in %s (tag expression %q); matched %d. A cell selecting none cannot be run; a cell selecting several is not addressable.",
			c.ID(p.Name), p.Feature, c.TagExpression(p.Name), len(matched))
		claimed[matched[0]]++
	}

	for i, n := range claimed {
		require.Equal(t, 1, n,
			"Examples block %d of %s (tags %v) is claimed by %d registry cells — a block no cell claims runs with its expected state recorded nowhere",
			i, p.Feature, blocks[i], n)
	}
}

// approachOfVariant maps a registry variant back to the approach it pins. The
// registry stores the variant (its cell name); the approach is the variant's
// leading segment, which approachVariantNamesItsApproach is the runtime half of.
func approachOfVariant(t *testing.T, variant string) string {
	t.Helper()
	require.NotEmpty(t, variant, "every P1 cell must carry a variant: it is what distinguishes cells sharing an engine and both axes")
	for _, a := range []string{"system-prompt", "unsafe-file", "hook"} {
		if strings.HasPrefix(variant, a) {
			return a
		}
	}
	t.Fatalf("P1 variant %q names no known approach — the registry and agent.ApproachNames have drifted", variant)
	return ""
}

// featureExamplesTagSets returns, per Examples block, the FULL set of tags that
// select it: the feature's own tags, the scenario's, and the block's. godog
// matches a tag expression against that union, so anything less would compare
// against a different thing than the runner does.
func featureExamplesTagSets(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err, "P1 names a feature file that does not exist")
	defer f.Close()

	doc, err := gherkin.ParseGherkinDocument(f, (&messages.Incrementing{}).NewId)
	require.NoError(t, err, "%s does not parse", path)
	require.NotNil(t, doc.Feature)

	var featureTags []string
	for _, tag := range doc.Feature.Tags {
		featureTags = append(featureTags, tag.Name)
	}

	var out [][]string
	for _, child := range doc.Feature.Children {
		if child.Scenario == nil {
			continue
		}
		scenarioTags := append([]string{}, featureTags...)
		for _, tag := range child.Scenario.Tags {
			scenarioTags = append(scenarioTags, tag.Name)
		}
		for _, ex := range child.Scenario.Examples {
			tags := append([]string{}, scenarioTags...)
			for _, tag := range ex.Tags {
				tags = append(tags, tag.Name)
			}
			out = append(out, tags)
		}
	}
	return out
}

func tagSetContainsAll(have, want []string) bool {
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}
