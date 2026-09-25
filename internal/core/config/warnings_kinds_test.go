package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allWarningKinds is every kind Load can attach to a Warning. A kind
// missing from here is a kind nothing below checks; "keep it exhaustive" was
// the whole mechanism once, and a kind was added without it, so
// TestWarningKind_AllWarningKindsIsExhaustive now scans the declarations.
var allWarningKinds = []WarningKind{
	WarnKindRead,
	WarnKindParse,
	WarnKindValidate,
	WarnKindUnknownKey,
	WarnKindMigrationLossy,
	WarnKindLayerScope,
	WarnKindEnginelessAgent,
}

// TestWarningKind_AllWarningKindsIsExhaustive turns the list above from a
// promise into a check: every `WarnKind... WarningKind = "..."` declared in
// warnings.go must appear in allWarningKinds by its on-the-wire value, so a
// new kind cannot skip the fatal-class/fix-it gate by being left out here.
func TestWarningKind_AllWarningKindsIsExhaustive(t *testing.T) {
	dir, err := sourcedir.Dir()
	require.NoError(t, err)
	src, err := os.ReadFile(filepath.Join(dir, "warnings.go"))
	require.NoError(t, err)

	listed := make(map[WarningKind]bool, len(allWarningKinds))
	for _, k := range allWarningKinds {
		listed[k] = true
	}
	declared := regexp.MustCompile(`(?m)^\s*WarnKind\w+ WarningKind = "([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	require.NotEmpty(t, declared, "the scan must find the kind declarations, or it is checking nothing")
	for _, m := range declared {
		assert.True(t, listed[WarningKind(m[1])], "kind %q is declared in warnings.go but missing from allWarningKinds", m[1])
	}
	assert.Len(t, allWarningKinds, len(declared), "allWarningKinds carries a kind warnings.go no longer declares")
}

// The doc on WarningKind promises that every kind is fatal-class in strict
// mode and the fail-loudly gate depends on it: a kind that mapped to no fatal
// class would degrade silently on exactly the startup paths that exist to
// refuse a broken  Each must also carry an actionable remedy, since the
// abort listing prints one per finding.
func TestWarningKind_EveryKindIsFatalWithARemedy(t *testing.T) {
	for _, kind := range allWarningKinds {
		t.Run(string(kind), func(t *testing.T) {
			require.NotEmpty(t, string(kind), "a kind's on-the-wire value must not be empty")
			assert.Contains(t,
				[]report.Kind{report.KindConfig, report.KindMigration},
				kind.Kind(),
				"every warning kind must bucket into a fatal class")
			assert.NotEmpty(t, kind.Remedy(), "every warning kind must name its fix")
		})
	}
}

// The drift this guards is a specific one: the type doc used to hand-maintain
// a COUNT of the kinds ("All four kinds are fatal-class"), a fifth kind was
// added, and the sentence quietly became false — a doc claiming a property of
// a set it no longer describes. Prose stating the invariant needs no number,
// so no number may appear.
//
// The source root is resolved from this test file's COMPILED-IN path rather
// than the working directory: a cwd-relative scan silently finds nothing when
// something moves the cwd, and a gate that finds nothing passes.
func TestWarningKind_DocStatesTheInvariantWithoutHandCountingKinds(t *testing.T) {
	dir, err := sourcedir.Dir()
	require.NoError(t, err, "cannot resolve this package's source directory")
	src, err := os.ReadFile(filepath.Join(dir, "warnings.go"))
	require.NoError(t, err)

	_, after, found := strings.Cut(string(src), "// WarningKind classifies")
	require.True(t, found, "WarningKind's doc comment must exist")
	doc, _, found := strings.Cut(after, "\ntype WarningKind string")
	require.True(t, found, "WarningKind's declaration must follow its doc comment")

	for _, numeral := range []string{"two", "three", "four", "five", "six", "seven"} {
		assert.NotContains(t, strings.ToLower(doc), " "+numeral+" kind",
			"the doc must state the invariant over ALL kinds, not count them by hand")
	}
}

// --- Warning.Finding / ReportWarnings ---------------------------------------
//
// ReportWarnings is how `ctxloom run`, `ctxloom mcp`, and the GetConfig-based
// command entrypoints turn a loaded config's warnings into findings the sink
// renders and ledgers, so a present-but-broken config cannot open a session
// that silently runs on empty context. The rendering itself (the "<prog>:
// warning:" line, the ledger's per-window dedup) is the sink's contract,
// pinned beside strictness.Sink; here we pin what config hands it.

func TestReportWarnings_OneFatalOnceFindingPerWarning(t *testing.T) {
	var sink report.Collector
	ReportWarnings(&sink, []Warning{
		{Kind: WarnKindParse, Text: "yaml is malformed: yaml: line 3: mapping values are not allowed"},
		{Kind: WarnKindValidate, Text: "profile \"dev\" failed schema validation"},
	})

	found := sink.All()
	require.Len(t, found, 2, "one finding per warning")
	for _, f := range found {
		assert.Equal(t, report.KindConfig, f.Kind, "a parse or validate warning is config-class")
		assert.True(t, f.Once, "one broken config file is one finding, however many times the config is loaded")
		assert.NotEmpty(t, f.Remedy, "the finding carries a fix-it hint")
	}
	assert.Equal(t, "yaml is malformed: yaml: line 3: mapping values are not allowed", found[0].Text, "the finding IS the warning text")
	assert.Contains(t, found[1].Text, "failed schema validation")
}

func TestWarning_Finding_UnknownKeyIsFatalAndNamesTheKey(t *testing.T) {
	f := Warning{
		Kind: WarnKindUnknownKey,
		Text: "unknown key `profiles.defaults` in /p/.ctxloom/yaml: ctxloom does not know it, so it is IGNORED — `profiles.defaults` was RETIRED",
	}.Finding()

	assert.True(t, f.Fatal(), "an unknown key is a fatal startup finding, not a silent drop")
	assert.Equal(t, report.KindConfig, f.Kind, "an unknown key is config-class")
	assert.Contains(t, f.Text, "profiles.defaults", "the finding names the offending key")
	assert.Contains(t, f.Remedy, "yaml", "the finding tells the user where to make the edit")
}

func TestWarning_Finding_LossyMigrationIsMigrationClass(t *testing.T) {
	f := Warning{Kind: WarnKindMigrationLossy, Text: "dropped setting x"}.Finding()
	assert.Equal(t, report.KindMigration, f.Kind)
}

func TestReportWarnings_NoWarningsReportsNothing(t *testing.T) {
	var sink report.Collector
	ReportWarnings(&sink, nil)
	assert.Empty(t, sink.All())
}

// A warning whose raise site knows a specific fix (a layer-scope violation's
// exact edit) carries it to the finding; one that does not falls to its
// kind's generic remedy.
func TestWarning_FindingCarriesTheSpecificRemedy(t *testing.T) {
	const specific = "Remove it from /p/config.yaml; set it in /h/config.yaml instead."
	w := Warning{Kind: WarnKindLayerScope, Text: "dropped", Remedy: specific}
	assert.Equal(t, specific, w.Finding().Remedy)
	plain := Warning{Kind: WarnKindRead, Text: "unreadable"}
	assert.Equal(t, WarnKindRead.Remedy(), plain.Finding().Remedy)
}
