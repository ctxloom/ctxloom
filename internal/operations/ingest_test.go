package operations

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/resources"
)

// These tests assert on the ASSEMBLED CONTEXT — the bytes AssembleContext
// actually hands a session — not on any internal counter. A dedup that
// bookkeeps correctly and still ships the fragment twice is the failure mode
// worth catching, and only the delivered bytes catch it.

// builtinIsolationContent reads the REAL embedded isolation fragment's bytes
// (resources/builtin_bundles/isolation.yaml), the one
// ResolveBuiltinBundleFragments injects into every session unconditionally.
// Tests that need "a fragment that is ALSO injected" must use these exact
// bytes, not a stand-in: the whole question is whether two routes to ONE piece
// of content collapse.
func builtinIsolationContent(t *testing.T) string {
	t.Helper()
	raw, err := resources.GetBuiltinBundle("isolation")
	require.NoError(t, err, "resources/builtin_bundles/isolation.yaml must be embedded")
	var b bundles.Bundle
	require.NoError(t, yaml.Unmarshal(raw, &b))
	frag, ok := b.Fragments["isolation-axes"]
	require.True(t, ok, "isolation.yaml must ship the isolation-axes fragment")
	require.NotEmpty(t, frag.Content)
	return strings.TrimSpace(frag.Content)
}

// writeIngestBundle drops a bundle into the test project's bundles dir.
func writeIngestBundle(t *testing.T, fs afero.Fs, name, body string) {
	t.Helper()
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0o755))
	require.NoError(t, afero.WriteFile(fs,
		paths.LocalBundlesPath(testBaseDir)+"/"+name+".yaml", []byte(body), 0o644))
}

// ingestLoader rebuilds a loader over fs after extra bundles have been written.
func ingestLoader(fs afero.Fs) *bundles.Loader {
	return bundles.NewLoader(bundles.NewProjectReader(fs, []string{paths.LocalBundlesPath(testBaseDir)}))
}

// TestIngest_InjectedBuiltinAlsoSelectedByRefIsAssembledOnce is THE provoking
// case, and the reason ingest idempotence exists at all.
//
// A project bundle named "isolation" ships a fragment "isolation-axes" whose
// bytes are the builtin's bytes — which is exactly the situation a profile
// selecting "isolation#fragments/isolation-axes" produces once the builtin
// reader is admitted to Config.BundleLoader. The two routes carry DIFFERENT
// ref strings ("ctxloom:local@bundles/isolation#fragments/isolation-axes" from
// the loader, "builtin:isolation#fragments/isolation-axes" from the injection),
// so neither dedupeFragmentRefs nor any string comparison of the refs collapses
// them. Only the ingest identity rule does.
func TestIngest_InjectedBuiltinAlsoSelectedByRefIsAssembledOnce(t *testing.T) {
	fs, _ := setupContextTestFS(t)
	body := builtinIsolationContent(t)
	writeIngestBundle(t, fs, "isolation", "version: \"1.0\"\nfragments:\n  isolation-axes:\n    content: |\n"+indentYAML(body))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	cfg = withProfileDefs(cfg, map[string]config.Profile{
		"picks-isolation": {Fragments: []config.FragmentRef{
			{Name: "isolation#fragments/isolation-axes"},
		}},
	})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "picks-isolation",
		Pipeline: opPipe(cfg, ingestLoader(fs)),
	})
	require.NoError(t, err)

	require.Contains(t, result.Context, body, "sanity: the isolation content must reach the context at all")
	assert.Equal(t, 1, strings.Count(result.Context, body),
		"a fragment that is both INJECTED as a builtin and SELECTED by ref must be assembled ONCE")
}

// TestIngest_SameFragmentSelectedByTwoProfilesIsAssembledOnce covers the
// inheritance-chain case: two composed profiles both select the same fragment.
func TestIngest_SameFragmentSelectedByTwoProfilesIsAssembledOnce(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	_ = fs

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	cfg = withProfileDefs(cfg, map[string]config.Profile{
		"a": {Fragments: []config.FragmentRef{{Name: "dev#fragments/security-rules"}}},
		"b": {Fragments: []config.FragmentRef{{Name: "dev#fragments/security-rules"}}},
	})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profiles: []string{"a", "b"},
		Pipeline: opPipe(cfg, loader),
	})
	require.NoError(t, err)

	assert.Equal(t, 1, strings.Count(result.Context, "Always validate input"),
		"one fragment selected by two profiles must be assembled ONCE")
}

// TestIngest_TwoDifferentFragmentsWithIdenticalContentBothSurvive pins the
// half of the identity rule that content-hash dedup gets wrong. These are two
// separately authored items; delivering one of them is data loss.
func TestIngest_TwoDifferentFragmentsWithIdenticalContentBothSurvive(t *testing.T) {
	fs, _ := setupContextTestFS(t)
	writeIngestBundle(t, fs, "twins", `version: "1.0"
fragments:
  twin-one:
    content: "IDENTICAL-TWIN-BODY"
  twin-two:
    content: "IDENTICAL-TWIN-BODY"
`)

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	cfg = withProfileDefs(cfg, map[string]config.Profile{
		"twins": {Fragments: []config.FragmentRef{
			{Name: "twins#fragments/twin-one"},
			{Name: "twins#fragments/twin-two"},
		}},
	})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "twins",
		Pipeline: opPipe(cfg, ingestLoader(fs)),
	})
	require.NoError(t, err)

	assert.Equal(t, 2, strings.Count(result.Context, "IDENTICAL-TWIN-BODY"),
		"two DIFFERENT fragments whose content happens to match are two fragments; both must be assembled")
}

// TestIngest_SameNameFromTwoBundlesBothSurvive pins the other half: the item
// identity carries the BUNDLE, so one name shipped by two bundles is two items
// even when their bytes are identical.
func TestIngest_SameNameFromTwoBundlesBothSurvive(t *testing.T) {
	fs, _ := setupContextTestFS(t)
	writeIngestBundle(t, fs, "pub-a", `version: "1.0"
fragments:
  standards:
    content: "SHARED-STANDARDS-BODY"
`)
	writeIngestBundle(t, fs, "pub-b", `version: "1.0"
fragments:
  standards:
    content: "SHARED-STANDARDS-BODY"
`)

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	cfg = withProfileDefs(cfg, map[string]config.Profile{
		"both": {Fragments: []config.FragmentRef{
			{Name: "pub-a#fragments/standards"},
			{Name: "pub-b#fragments/standards"},
		}},
	})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "both",
		Pipeline: opPipe(cfg, ingestLoader(fs)),
	})
	require.NoError(t, err)

	assert.Equal(t, 2, strings.Count(result.Context, "SHARED-STANDARDS-BODY"),
		"the same fragment NAME from two different bundles is two items; both must be assembled")
}

// TestIngest_OrderIsUnchangedByTheDuplicate is the order proof, and it is
// deliberately an equality on the WHOLE assembled string rather than a
// containment check: adding a second route to content already in the context
// must leave the delivered bytes byte-for-byte identical. A dedup that keeps
// the LAST occurrence instead of the first, or that rebuilds the surviving
// list out of a map, fails here — those reorder the context without changing
// what it contains.
func TestIngest_OrderIsUnchangedByTheDuplicate(t *testing.T) {
	body := builtinIsolationContent(t)

	assemble := func(t *testing.T, refs []config.FragmentRef) string {
		t.Helper()
		fs, _ := setupContextTestFS(t)
		writeIngestBundle(t, fs, "isolation", "version: \"1.0\"\nfragments:\n  isolation-axes:\n    content: |\n"+indentYAML(body))
		cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
		cfg = withProfileDefs(cfg, map[string]config.Profile{"p": {Fragments: refs}})
		result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
			Profile:  "p",
			Pipeline: opPipe(cfg, ingestLoader(fs)),
		})
		require.NoError(t, err)
		return result.Context
	}

	without := assemble(t, []config.FragmentRef{
		{Name: "dev#fragments/security-rules"},
		{Name: "dev#fragments/go-patterns"},
	})
	with := assemble(t, []config.FragmentRef{
		{Name: "dev#fragments/security-rules"},
		{Name: "isolation#fragments/isolation-axes"},
		{Name: "dev#fragments/go-patterns"},
	})

	require.Contains(t, without, body, "sanity: the builtin injects even without the explicit selection")
	assert.Equal(t, without, with,
		"selecting a fragment that is ALREADY injected must not change one byte of the assembled context")
}

// TestIngest_DropIsSilentForTheSameRefAndSpeaksForADifferentOne pins the
// silence decision. Re-selecting the SAME ref says nothing (the two asks were
// textually identical; there is no mistake to surface, and warning would put a
// line on stderr for every configuration that composes overlapping profiles).
// Two DIFFERENT refs collapsing DOES warn: that is the only case where the user
// wrote two different selections and got one fragment, so "I meant two
// different fragments" is a live reading that a silent drop would mask.
func TestIngest_DropIsSilentForTheSameRefAndSpeaksForADifferentOne(t *testing.T) {
	body := builtinIsolationContent(t)

	// captureIngestWarnings swaps the accumulator's diagnostic sink for the
	// duration of fn. Asserting through clidiag's process-global WarnOnce set
	// would be order-dependent: this diagnostic's dedup key is the two refs
	// alone, so whichever test in the package collapses a given pair first
	// consumes the key and every later one observes silence it did not cause.
	captureIngestWarnings := func(t *testing.T, fn func()) []string {
		t.Helper()
		var lines []string
		prev := ingestWarn
		ingestWarn = func(format string, args ...any) {
			lines = append(lines, fmt.Sprintf(format, args...))
		}
		defer func() { ingestWarn = prev }()
		fn()
		return lines
	}

	// The same-ref arrival is asserted on the accumulator directly, not through
	// AssembleContext, and that is a measurement rather than a convenience:
	// dedupeFragmentRefs collapses two identical ref strings BEFORE ingest, so
	// no assembly reachable today calls add twice with one ref. The branch is
	// still live code and still has to be right — this is the ingest layer, and
	// it may not assume a caller pre-deduped for it — so it is exercised where
	// it can be reached. Routing this through AssembleContext instead would
	// assert nothing at all: the drop would happen upstream and the silence
	// would be an artefact, not a decision.
	t.Run("same ref twice: dropped, and silent", func(t *testing.T) {
		lines := captureIngestWarnings(t, func() {
			in := newContextIngest()
			require.True(t, in.add(ingestedFragment{Ref: "dev#fragments/rules", Name: "dev/rules", Content: "RULES"}))
			require.False(t, in.add(ingestedFragment{Ref: "dev#fragments/rules", Name: "dev/rules", Content: "RULES"}),
				"a re-ingest of the same ref is a duplicate and must be dropped")
			assert.Equal(t, "RULES", in.join(), "and delivered once")
		})
		assert.Empty(t, lines, "re-selecting the same ref is unambiguous; it must not warn")
	})

	t.Run("two different refs, one item: dropped at the accumulator, and it says so", func(t *testing.T) {
		lines := captureIngestWarnings(t, func() {
			in := newContextIngest()
			require.True(t, in.add(ingestedFragment{Ref: "ctxloom:local@bundles/dev#fragments/rules", Name: "dev/rules", Content: "RULES"}))
			require.False(t, in.add(ingestedFragment{Ref: "builtin:dev#fragments/rules", Name: "builtin dev/rules", Content: "RULES"}))
		})
		require.Len(t, lines, 1)
		assert.Contains(t, lines[0], "reached this context twice")
	})

	t.Run("two different refs, one item: warns and names both", func(t *testing.T) {
		fs, _ := setupContextTestFS(t)
		writeIngestBundle(t, fs, "isolation", "version: \"1.0\"\nfragments:\n  isolation-axes:\n    content: |\n"+indentYAML(body))
		cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
		cfg = withProfileDefs(cfg, map[string]config.Profile{
			"p": {Fragments: []config.FragmentRef{{Name: "isolation#fragments/isolation-axes"}}},
		})
		lines := captureIngestWarnings(t, func() {
			_, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
				Profile:  "p",
				Pipeline: opPipe(cfg, ingestLoader(fs)),
			})
			require.NoError(t, err)
		})
		require.Len(t, lines, 1, "one collapse under two spellings must say so exactly once")
		assert.Contains(t, lines[0], "reached this context twice")
		assert.Contains(t, lines[0], "ctxloom:local@bundles/isolation#fragments/isolation-axes",
			"the warning must name the occurrence that was KEPT")
		assert.Contains(t, lines[0], builtinIsolationFragmentRef,
			"the warning must name the occurrence that was DROPPED")
	})
}

// TestIngest_CollapsedDuplicateStaysReportedAsLoaded guards the reporting seam.
// A fragment whose second copy was dropped still had its content delivered, via
// the occurrence that survived — so naming it missing, or omitting it from
// FragmentsLoaded, would make warnGuttedProfiles accuse a profile of
// contributing nothing when its fragment is right there in the context.
func TestIngest_CollapsedDuplicateStaysReportedAsLoaded(t *testing.T) {
	fs, _ := setupContextTestFS(t)
	body := builtinIsolationContent(t)
	writeIngestBundle(t, fs, "isolation", "version: \"1.0\"\nfragments:\n  isolation-axes:\n    content: |\n"+indentYAML(body))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	cfg = withProfileDefs(cfg, map[string]config.Profile{
		"p": {Fragments: []config.FragmentRef{{Name: "isolation#fragments/isolation-axes"}}},
	})

	stderr := captureStderr(t, func() {
		result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
			Profile:  "p",
			Pipeline: opPipe(cfg, ingestLoader(fs)),
		})
		require.NoError(t, err)
		assert.Contains(t, result.FragmentsLoaded, "ctxloom:local@bundles/isolation#fragments/isolation-axes")
		assert.Contains(t, result.FragmentsLoaded, builtinIsolationFragmentRef,
			"the dropped occurrence still LOADED and its content is in the context")
		assert.Empty(t, result.MissingFragments)
	})
	assert.NotContains(t, stderr, "contributed NO content",
		"a profile whose fragment collapsed into an identical one is not gutted")
}

// TestIngestItemKey_IdentityIsSourceAgnosticAndSelectorBearing pins the exact
// reduction the identity rule depends on, at the unit level: the builtin and
// loader spellings of ONE item reduce to the same key, and every distinction
// the rule must preserve survives the reduction.
func TestIngestItemKey_IdentityIsSourceAgnosticAndSelectorBearing(t *testing.T) {
	builtin := ingestItemKey("builtin:isolation#fragments/isolation-axes")
	local := ingestItemKey("ctxloom:local@bundles/isolation#fragments/isolation-axes")
	bare := ingestItemKey("isolation#fragments/isolation-axes")

	assert.Equal(t, builtin, local, "the builtin and loader spellings of ONE item must reduce to one key")
	assert.Equal(t, builtin, bare, "the bare local spelling must reduce to the same key too")

	assert.NotEqual(t, builtin, ingestItemKey("builtin:isolation#fragments/other-axes"),
		"a different item NAME is a different item")
	assert.NotEqual(t, builtin, ingestItemKey("builtin:other-bundle#fragments/isolation-axes"),
		"a different BUNDLE is a different item")
	assert.NotEqual(t, builtin, ingestItemKey("builtin:isolation#commands/isolation-axes"),
		"a different item KIND is a different item")

	// A ref the grammar cannot parse is used verbatim, so it can only ever
	// match a byte-identical spelling — never a different one.
	assert.Equal(t, "not a ref", ingestItemKey("not a ref"))
}

// TestIngest_JoinOmitsBlankSectionsAndFragmentsKeepsThem pins the deliberate
// split between the two readers of the accumulator: the assembled STRING must
// not carry a "---" separator with nothing on one side of it, while the
// fragment LIST must still report a blank fragment so a delivery path can tell
// "no context was configured" from "everything configured resolved to nothing".
func TestIngest_JoinOmitsBlankSectionsAndFragmentsKeepsThem(t *testing.T) {
	in := newContextIngest()
	require.True(t, in.add(ingestedFragment{Ref: "b#fragments/blank", Name: "b/blank", Content: "   \n "}))
	require.True(t, in.add(ingestedFragment{Ref: "b#fragments/real", Name: "b/real", Content: "REAL"}))

	assert.Equal(t, "REAL", in.join(), "a blank section must not be framed by a separator")
	assert.Len(t, in.fragments(), 2, "the blank fragment is still an ingested fragment")
}

// indentYAML indents a block for embedding under a YAML literal scalar.
func indentYAML(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		b.WriteString("      ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// ---- the SECOND accumulation point: regenerateContext → the context file ----
//
// regenerateContext assembles the SessionStart-injected context file through
// its own loop, with its own builtin injection at the end. It must obey the
// same rule, and its output is a file, so these assert on the bytes actually
// written — the thing a session loads.

// TestIngest_RegenerateContext_InjectedBuiltinAlsoSelectedByRefIsWrittenOnce is
// the provoking case on the SessionStart path.
func TestIngest_RegenerateContext_InjectedBuiltinAlsoSelectedByRefIsWrittenOnce(t *testing.T) {
	body := builtinIsolationContent(t)
	appDir, workDir := regenTestApp(t)
	writeRegenBundle(t, appDir, "isolation",
		"version: \"1.0\"\nfragments:\n  isolation-axes:\n    content: |\n"+indentYAML(body))

	cfg := config.NewFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"default": {Fragments: []config.FragmentRef{{Name: "isolation#fragments/isolation-axes"}}},
		}},
	})

	hash, err := regenerateContext(cfg, workDir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	written, err := agent.ReadContextFile(workDir, hash)
	require.NoError(t, err)
	require.Contains(t, written, body, "sanity: the isolation content must reach the context file")
	assert.Equal(t, 1, strings.Count(written, body),
		"the SessionStart context file must carry an injected-AND-selected fragment once")
}

// TestIngest_RegenerateContext_TwoDifferentFragmentsWithIdenticalContentBothSurvive
// is the case the previous content-hash dedup got wrong all the way to the
// delivered file: two separately authored fragments that happen to say the same
// thing were collapsed to one, and the second publisher's item never reached
// the session.
func TestIngest_RegenerateContext_TwoDifferentFragmentsWithIdenticalContentBothSurvive(t *testing.T) {
	appDir, workDir := regenTestApp(t)
	writeRegenBundle(t, appDir, "twins", `version: "1.0"
fragments:
  twin-one:
    content: "IDENTICAL-TWIN-BODY"
  twin-two:
    content: "IDENTICAL-TWIN-BODY"
`)

	cfg := config.NewFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"default": {Fragments: []config.FragmentRef{
				{Name: "twins#fragments/twin-one"},
				{Name: "twins#fragments/twin-two"},
			}},
		}},
	})

	hash, err := regenerateContext(cfg, workDir, nil)
	require.NoError(t, err)
	written, err := agent.ReadContextFile(workDir, hash)
	require.NoError(t, err)

	assert.Equal(t, 2, strings.Count(written, "IDENTICAL-TWIN-BODY"),
		"two different fragments that merely say the same thing must BOTH reach the context file")
}

// TestIngest_RegenerateContext_OrderIsUnchangedByTheDuplicate is the order
// proof on the SessionStart path: the context file is content-addressed, so an
// identical hash is proof the bytes did not move.
func TestIngest_RegenerateContext_OrderIsUnchangedByTheDuplicate(t *testing.T) {
	body := builtinIsolationContent(t)

	regen := func(t *testing.T, refs []config.FragmentRef) (hash, written string) {
		t.Helper()
		appDir, workDir := regenTestApp(t)
		writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  alpha:
    content: "ALPHA-BODY"
  omega:
    content: "OMEGA-BODY"
`)
		writeRegenBundle(t, appDir, "isolation",
			"version: \"1.0\"\nfragments:\n  isolation-axes:\n    content: |\n"+indentYAML(body))
		cfg := config.NewFixture(config.Fixture{
			AppPaths:     []string{appDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
			Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
				"default": {Fragments: refs},
			}},
		})
		h, err := regenerateContext(cfg, workDir, nil)
		require.NoError(t, err)
		w, err := agent.ReadContextFile(workDir, h)
		require.NoError(t, err)
		return h, w
	}

	withoutHash, without := regen(t, []config.FragmentRef{
		{Name: "dev#fragments/alpha"},
		{Name: "dev#fragments/omega"},
	})
	withHash, with := regen(t, []config.FragmentRef{
		{Name: "dev#fragments/alpha"},
		{Name: "isolation#fragments/isolation-axes"},
		{Name: "dev#fragments/omega"},
	})

	require.Contains(t, without, body, "sanity: the builtin injects even without the explicit selection")
	assert.Equal(t, without, with,
		"selecting a fragment that is ALREADY injected must not move one byte of the context file")
	assert.Equal(t, withoutHash, withHash,
		"the context file is content-addressed: an unchanged hash is an unchanged file")
}

// TestIngest_SameItemWithDifferentContentBothSurvive is the case that makes the
// CONTENT half of the identity rule load-bearing, and it is the trust-relevant
// one: a project bundle named "isolation" shipping its own "isolation-axes"
// with DIFFERENT bytes is the same ITEM identity as the builtin, arriving from
// a different source. Collapsing on item identity alone would silently deliver
// one source's bytes in place of the other's — a substitution the user never
// asked for and could not see. Both survive, so no source's content is ever
// stood in for by another's.
func TestIngest_SameItemWithDifferentContentBothSurvive(t *testing.T) {
	builtinBody := builtinIsolationContent(t)
	fs, _ := setupContextTestFS(t)
	writeIngestBundle(t, fs, "isolation", `version: "1.0"
fragments:
  isolation-axes:
    content: "LOCAL-ISOLATION-OVERRIDE"
`)

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	cfg = withProfileDefs(cfg, map[string]config.Profile{
		"p": {Fragments: []config.FragmentRef{{Name: "isolation#fragments/isolation-axes"}}},
	})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "p",
		Pipeline: opPipe(cfg, ingestLoader(fs)),
	})
	require.NoError(t, err)

	assert.Contains(t, result.Context, "LOCAL-ISOLATION-OVERRIDE",
		"the selected fragment's own bytes must be delivered")
	assert.Contains(t, result.Context, builtinBody,
		"the injected builtin's bytes must NOT be dropped in favour of a same-named item with different content")
}

// TestIngest_FirstOccurrenceIsTheOneKept proves WHICH occurrence survives, by
// its POSITION in the assembled bytes. The two occurrences are byte-identical
// by definition of a duplicate, so nothing about the content can distinguish
// first-wins from last-wins — only where the content lands can.
//
// The profile selects the duplicated fragment FIRST and a second fragment
// after it; the always-on builtin injection then arrives LAST. Keeping the
// first occurrence leaves the content where the profile put it, ahead of the
// other fragment. Keeping the last would move it to the end of the context,
// past everything selected after it — a different context for the same
// configuration.
func TestIngest_FirstOccurrenceIsTheOneKept(t *testing.T) {
	body := builtinIsolationContent(t)
	fs, _ := setupContextTestFS(t)
	writeIngestBundle(t, fs, "isolation", "version: \"1.0\"\nfragments:\n  isolation-axes:\n    content: |\n"+indentYAML(body))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	cfg = withProfileDefs(cfg, map[string]config.Profile{
		"p": {Fragments: []config.FragmentRef{
			{Name: "isolation#fragments/isolation-axes"},
			{Name: "dev#fragments/security-rules"},
		}},
	})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "p",
		Pipeline: opPipe(cfg, ingestLoader(fs)),
	})
	require.NoError(t, err)

	iso := strings.Index(result.Context, body)
	sec := strings.Index(result.Context, "Always validate input")
	require.NotEqual(t, -1, iso)
	require.NotEqual(t, -1, sec)
	assert.Less(t, iso, sec,
		"the FIRST occurrence is kept: the fragment stays where the profile selected it, not at the end where the builtin injection would have put it")
	assert.Equal(t, 1, strings.Count(result.Context, body), "and still only once")
}
