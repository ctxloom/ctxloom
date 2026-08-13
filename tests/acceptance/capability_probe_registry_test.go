// Untagged, like the registry it checks: `just test` walks this without a
// built binary, a live engine or a paid turn.
//
// WHAT THIS FILE DEFENDS AGAINST. The failure mode of a capability suite is
// never a red test — a red test is the suite working. It is a capability nobody
// wrote a probe for, which produces no output at all and is indistinguishable
// from a capability that passes. Every check below turns one flavour of that
// silence into a hermetic red:
//
//   - an inventory row no probe reaches, and no human excused → red;
//   - an excuse for a row that IS probed (a stale parked entry) → red;
//   - a cell claiming live-verified with no evidence of what was measured → red;
//   - a gated-out cell with no declaring symbol → red;
//   - a red-mapped cell with no recorded failure shape, so a weekly sweep could
//     only count reds instead of diffing them → red;
//   - a registry row and its feature file disagreeing about which cells exist →
//     red, in both directions.
//
// That last one is what makes "container cells are red-expected" a conscious
// state rather than a rotting one: flipping a cell green-expected, or deleting
// one because it fails, has to be typed here and there together.
package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	gherkin "github.com/cucumber/gherkin/go/v26"
	messages "github.com/cucumber/messages/go/v21"
	"github.com/stretchr/testify/require"
)

// TestProbeRegistry_EveryCapabilityIsProbedOrExcused is the completeness gate:
// every row of the §1 capability inventory maps to at least one probe, or to a
// written reason for why it does not. Checked in BOTH directions, the
// assertExactUncovered idiom — an excuse for a capability that is in fact
// probed is stale bookkeeping, and stale bookkeeping is how an excuse list
// becomes a place work goes to be forgotten.
func TestProbeRegistry_EveryCapabilityIsProbedOrExcused(t *testing.T) {
	coverage := probeCapabilityCoverage()

	for _, row := range capabilityInventory {
		probes := coverage[row.Num]
		excuse, excused := capabilitiesProvenElsewhere[row.Num]

		switch {
		case len(probes) == 0 && !excused:
			t.Errorf("capability %d (%s) is reached by NO probe and has no entry in capabilitiesProvenElsewhere — an unprobed capability produces no output and is indistinguishable from one that passes. Add a probe row, or write down why this one does not need a cell.",
				row.Num, row.Symbol)
		case len(probes) > 0 && excused:
			t.Errorf("capability %d (%s) is BOTH probed (%v) and excused (%q) — the excuse is stale; prune it, or the list becomes a place work goes to be forgotten",
				row.Num, row.Symbol, probes, excuse)
		case excused:
			if len(strings.Fields(excuse)) < 8 {
				t.Errorf("capability %d (%s): the excuse %q is too terse to be an argument. Whoever reads this next needs to know what proves the capability instead.",
					row.Num, row.Symbol, excuse)
			}
			t.Logf("capability %d — NOT PROBED, excused: %s", row.Num, excuse)
		default:
			t.Logf("capability %d — probed by %v", row.Num, probes)
		}
	}

	// An excuse naming a row that does not exist is a renumbering accident: it
	// silently stops excusing anything.
	known := map[int]bool{}
	for _, row := range capabilityInventory {
		known[row.Num] = true
	}
	for num := range capabilitiesProvenElsewhere {
		if !known[num] {
			t.Errorf("capabilitiesProvenElsewhere excuses capability %d, which is not in the inventory — it is excusing nothing", num)
		}
	}
}

// TestProbeRegistry_CapabilityInventoryIsWellFormed keeps the inventory itself
// honest: numbers are the stable handle probes cite, so a duplicate or a gap
// makes a probe's claim ambiguous.
func TestProbeRegistry_CapabilityInventoryIsWellFormed(t *testing.T) {
	require.NotEmpty(t, capabilityInventory, "an empty inventory would make the completeness gate vacuously green")
	seen := map[int]bool{}
	for i, row := range capabilityInventory {
		if seen[row.Num] {
			t.Errorf("capability number %d appears twice — probes cite these numbers, so a duplicate makes a claim ambiguous", row.Num)
		}
		seen[row.Num] = true
		if row.Num != i+1 {
			t.Errorf("capability at index %d is numbered %d — numbering must be dense and in order so a probe citing a number cannot silently come to mean a different row", i, row.Num)
		}
		if !strings.Contains(row.Symbol, " — ") {
			t.Errorf("capability %d must name the SYMBOL that asks for it, then what it is (standing rule: cite by symbol, never by file:line); got %q", row.Num, row.Symbol)
		}
	}
}

// TestProbeRegistry_ProbeRowsAreWellFormed checks the shape of every row: the
// things a probe must have declared before its cells mean anything.
func TestProbeRegistry_ProbeRowsAreWellFormed(t *testing.T) {
	require.NotEmpty(t, probeRegistry, "an empty registry would make every check here vacuously green")

	names := map[string]bool{}
	knownCap := map[int]bool{}
	for _, row := range capabilityInventory {
		knownCap[row.Num] = true
	}

	for _, p := range probeRegistry {
		if names[p.Name] {
			t.Errorf("two probes are named %q — the name is the @probe- tag and the registry key, so a duplicate makes a cell unaddressable", p.Name)
		}
		names[p.Name] = true

		if strings.ToLower(p.Name) != p.Name || strings.ContainsAny(p.Name, " \t") {
			t.Errorf("probe name %q must be lowercase and whitespace-free: it becomes a gherkin tag, and a tag containing whitespace does not merely fail to match — it aborts the parse of the whole feature file", p.Name)
		}
		if p.Title == "" {
			t.Errorf("probe %q has no title — the registry report is read by a human deciding what to run", p.Name)
		}
		if p.Channel.Shape == "" || p.Channel.Where == "" {
			t.Errorf("probe %q does not declare where it plants its nonce. Every probe states its planting channel: a probe that cannot name its channel is not testing one, and its delivery failures cannot be attributed.", p.Name)
		}
		for _, n := range p.Capabilities {
			if !knownCap[n] {
				t.Errorf("probe %q claims capability %d, which is not in the inventory", p.Name, n)
			}
		}
		if !p.AssertionSide && len(p.Cells) == 0 {
			t.Errorf("probe %q declares no cells and is not assertion-side — a probe with no cells runs nothing while looking like coverage", p.Name)
		}
		if p.AssertionSide && p.Paid {
			t.Errorf("probe %q is assertion-side but marked paid: assertion-side probes fold into other probes' cells and buy none of their own", p.Name)
		}
	}
}

// TestProbeRegistry_CellsAreWellFormedAndEvidenced is the heart of it: a cell
// may not claim more than it can show.
func TestProbeRegistry_CellsAreWellFormedAndEvidenced(t *testing.T) {
	validStatus := map[probeStatus]bool{
		probePlanned: true, probeWired: true, probeLiveVerified: true,
		probeGatedOut: true, probeDeferred: true,
	}
	knownEngine := map[string]bool{}
	for _, e := range probeEngines {
		knownEngine[e] = true
	}

	for _, p := range probeRegistry {
		seen := map[probeCellID]bool{}
		for _, c := range p.Cells {
			id := c.ID(p.Name)

			if seen[id] {
				t.Errorf("%s: declared twice — two rows for one addressable cell means one of them never runs and nobody can tell which", id)
			}
			seen[id] = true

			if !knownEngine[c.Engine] {
				t.Errorf("%s: engine %q is not in probeEngines (%v) — a cell naming an unregistered engine skips forever and looks like coverage", id, c.Engine, probeEngines)
			} else if _, ok := liveAgents[backendTypeToLiveKey(c.Engine)]; !ok {
				t.Errorf("%s: engine %q resolves to liveAgents key %q, which is not registered — the cell could never be gated, let alone run", id, c.Engine, backendTypeToLiveKey(c.Engine))
			}
			if c.Runtime != "host" && c.Runtime != "container" {
				t.Errorf("%s: runtime %q is neither host nor container", id, c.Runtime)
			}
			if c.Workspace != "none" && c.Workspace != "worktree" {
				t.Errorf("%s: workspace %q is neither none nor worktree", id, c.Workspace)
			}
			if !validStatus[c.Status] {
				t.Errorf("%s: status %q is not one of planned/wired/live-verified/gated-out/deferred", id, c.Status)
			}

			// The evidence rules. Each exists because the status makes a claim
			// that is worthless — or actively misleading — without it.
			switch c.Status {
			case probeLiveVerified:
				if c.Reason == "" {
					t.Errorf("%s claims LIVE-VERIFIED with no evidence. An unevidenced green claim is worse than a blank: it stops the next person from looking. Say what was measured and when.", id)
				}
			case probeGatedOut:
				if c.Reason == "" {
					t.Errorf("%s is gated-out with no reason. A declared absence must name the symbol that declares it (noHooksReason, unsupportedHookKinds, resolveResumeMode), or it is indistinguishable from a capability someone forgot.", id)
				}
			case probeDeferred:
				if c.Reason == "" {
					t.Errorf("%s is deferred with no reason — deferred work with no stated reason is just missing work", id)
				}
			}

			// The red map. A recorded expected-failure is what lets a weekly
			// sweep DIFF shapes instead of counting reds; a shape with no story
			// behind it cannot be diffed by a human six weeks later.
			if c.ExpectedFailure != "" {
				if c.ExpectedFailureNote == "" {
					t.Errorf("%s is red-mapped as %q with no note. A sweep diffs failure SHAPES; a shape nobody explained cannot be judged when it changes.", id, c.ExpectedFailure)
				}
				if c.Status == probeGatedOut || c.Status == probeDeferred {
					t.Errorf("%s is %s AND red-mapped — a cell that never runs cannot have a measured failure shape; one of the two is wrong", id, c.Status)
				}
				if c.Status == probeLiveVerified {
					t.Errorf("%s claims live-verified AND carries an expected failure — it cannot be both green and red-mapped", id)
				}
			}
		}
	}
}

// TestProbeRegistry_TagsSurviveTheGherkinParser. A tag containing whitespace
// does not fail to match — it aborts the parse of the entire feature file and
// takes every scenario in it down, silently, which is the exact incident
// TestFeatureFilesParse was written for. Cell tags are generated from registry
// data, so the data is where the property has to hold.
func TestProbeRegistry_TagsSurviveTheGherkinParser(t *testing.T) {
	for _, p := range probeRegistry {
		for _, c := range p.Cells {
			for _, tag := range c.Tags(p.Name) {
				if !strings.HasPrefix(tag, "@") {
					t.Errorf("%s: tag %q does not start with @", c.ID(p.Name), tag)
				}
				if strings.ContainsAny(tag, " \t\n") {
					t.Errorf("%s: tag %q contains whitespace — the gherkin parser rejects the whole FILE on this, taking every scenario in it", c.ID(p.Name), tag)
				}
			}
			expr := c.TagExpression(p.Name)
			for _, want := range []string{"@live", "@probe-" + p.Name, "@" + c.Engine, "@" + c.Runtime, "@ws-" + c.Workspace} {
				if !strings.Contains(expr, want) {
					t.Errorf("%s: tag expression %q cannot select this cell (%s missing)", c.ID(p.Name), expr, want)
				}
			}
		}
	}
}

// TestProbeRegistry_WiredProbesMatchTheirFeatureFile closes the drift between
// the table and the thing that actually runs. A probe that names a feature must
// name one that exists, and its RUNNABLE cells (everything not gated-out or
// deferred) must be exactly the cells that feature declares — in both
// directions. A row in the table with no scenario is a claim nobody can run; a
// scenario with no row is a cell whose expected state nobody recorded.
func TestProbeRegistry_WiredProbesMatchTheirFeatureFile(t *testing.T) {
	for _, p := range probeRegistry {
		if p.Feature == "" {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			path := filepath.Join("features", p.Feature)
			f, err := os.Open(path)
			require.NoError(t, err, "probe %q names feature %q, which does not exist — the registry would be describing cells nobody can run", p.Name, p.Feature)
			defer f.Close()

			doc, err := gherkin.ParseGherkinDocument(f, (&messages.Incrementing{}).NewId)
			require.NoError(t, err, "%s does not parse", path)

			inFeature := featureCellKeys(doc)
			require.NotEmpty(t, inFeature, "%s declared no engine/runtime/workspace Examples rows — this check would compare against nothing and pass vacuously", path)

			inRegistry := map[string]bool{}
			for _, c := range p.Cells {
				if c.Status == probeGatedOut || c.Status == probeDeferred {
					continue
				}
				inRegistry[cellKey(c.Engine, c.Runtime, c.Workspace)] = true
			}

			for k := range inFeature {
				if !inRegistry[k] {
					t.Errorf("%s runs cell %s, which the registry does not declare — its expected state (green, or red-mapped with a shape) is recorded nowhere", path, k)
				}
			}
			for k := range inRegistry {
				if !inFeature[k] {
					t.Errorf("registry declares runnable cell %s for probe %q, but %s has no Examples row for it — the claim cannot be run", k, p.Name, path)
				}
			}
		})
	}
}

func cellKey(engine, runtime, workspace string) string {
	return fmt.Sprintf("%s/%s/%s", engine, runtime, workspace)
}

// featureCellKeys collects every Examples row of doc that carries engine,
// runtime and workspace columns. Column-name driven rather than positional, so
// a reordered table does not silently compare the wrong things.
func featureCellKeys(doc *messages.GherkinDocument) map[string]bool {
	out := map[string]bool{}
	if doc == nil || doc.Feature == nil {
		return out
	}
	for _, child := range doc.Feature.Children {
		if child.Scenario == nil {
			continue
		}
		for _, ex := range child.Scenario.Examples {
			if ex.TableHeader == nil {
				continue
			}
			col := map[string]int{}
			for i, c := range ex.TableHeader.Cells {
				col[strings.TrimSpace(c.Value)] = i
			}
			ei, eok := col["engine"]
			ri, rok := col["runtime"]
			wi, wok := col["workspace"]
			if !eok || !rok || !wok {
				continue
			}
			for _, row := range ex.TableBody {
				if len(row.Cells) <= ei || len(row.Cells) <= ri || len(row.Cells) <= wi {
					continue
				}
				out[cellKey(
					strings.TrimSpace(row.Cells[ei].Value),
					strings.TrimSpace(row.Cells[ri].Value),
					strings.TrimSpace(row.Cells[wi].Value),
				)] = true
			}
		}
	}
	return out
}

// TestProbeRegistry_ReportsItsOwnCost prints the ladder, and refuses runaway
// growth. Cost is a real constraint here — every paid cell is a subscription
// turn on a human's account — and the design's own rule is that a sweep beyond
// roughly ten cells gets stated before it runs. The bound is deliberately loose
// (the designed ladder is ~40 cells): it catches a wave that accidentally
// cross-products an axis, not ordinary growth.
func TestProbeRegistry_ReportsItsOwnCost(t *testing.T) {
	paid := probePaidCellCount()
	t.Logf("%s", formatProbeRegistryReport())
	t.Logf("CAPABILITY LADDER COST: %d paid cells (each one a real subscription turn)", paid)
	if paid > 60 {
		t.Errorf("the ladder now declares %d paid cells. The design budgets roughly 40; a jump past 60 is usually an axis accidentally cross-producted. State the cost and raise this bound deliberately, or fix the table.", paid)
	}

	// The report is the evidence artifact a sweep diffs and a human reads when
	// deciding what to run, so it must NAME every cell it was given. A renderer
	// that quietly drops rows would make the ladder look smaller than it is,
	// which is the same silence the completeness gate exists to break — just
	// moved one layer out.
	report := formatProbeRegistryReport()
	for _, p := range probeRegistry {
		for _, c := range p.Cells {
			if !strings.Contains(report, c.ID(p.Name).String()) {
				t.Errorf("the registry report does not name cell %s — a report that drops rows makes the ladder look smaller than it is", c.ID(p.Name))
			}
		}
	}
}

// TestProbeRegistry_SetCellRefusesToAnnotateACellThatIsNotThere guards the one
// place in this file where a measured finding can vanish without trace.
//
// setCell attaches the evidenced exceptions — kiro's ANSI-decoration red,
// opencode's flakiness, the container red-map — onto generated rows. If it ever
// stops matching (an engine renamed, an axis spelled differently), a silent
// miss would leave the generated table looking complete while the finding it
// was supposed to carry simply is not there: exactly the "work that never ran
// is indistinguishable from work that passed" failure this registry exists to
// break. It panics instead, at package init, and this is the test that says so.
func TestProbeRegistry_SetCellRefusesToAnnotateACellThatIsNotThere(t *testing.T) {
	cells := []probeCell{{Engine: "claude-code", Runtime: "host", Workspace: "none"}}

	t.Run("present cell is annotated", func(t *testing.T) {
		setCell(cells, "claude-code", "host", "none", func(c *probeCell) { c.Reason = "annotated" })
		require.Equal(t, "annotated", cells[0].Reason, "setCell must write through to the cell, or every evidenced exception is a no-op")
	})

	t.Run("absent cell panics rather than dropping the finding", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("setCell silently ignored a cell that is not there — the finding it carries would vanish while the table still looked complete")
			}
			if !strings.Contains(fmt.Sprint(r), "engine=kiro") {
				t.Fatalf("the panic must name the cell it could not find, got: %v", r)
			}
		}()
		setCell(cells, "kiro", "container", "worktree", func(c *probeCell) { c.Reason = "unreachable" })
	})
}

// TestProbeRegistry_LookupFindsWhatItDeclares guards the accessor the sweep
// runner and the step files will address cells through.
func TestProbeRegistry_LookupFindsWhatItDeclares(t *testing.T) {
	p, ok := probeSpecByName(probeP0)
	require.True(t, ok, "probeSpecByName must find the floor probe %q; every wave-2 slice addresses its own row this way", probeP0)
	require.Equal(t, "engine_isolation_matrix.feature", p.Feature)

	if _, ok := probeSpecByName("p99-does-not-exist"); ok {
		t.Error("probeSpecByName must report a miss, not hand back a zero-value probe a caller would then treat as real")
	}

	// Sorted names, logged: the cheapest possible map of what exists.
	var names []string
	for _, s := range probeRegistry {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	t.Logf("probes: %s", strings.Join(names, ", "))
}
