package config

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
// refuse a broken  Each must also carry an actionable fix-it, since the
// abort listing prints one per finding.
func TestWarningKind_EveryKindIsFatalClassWithAFixIt(t *testing.T) {
	for _, kind := range allWarningKinds {
		t.Run(string(kind), func(t *testing.T) {
			require.NotEmpty(t, string(kind), "a kind's on-the-wire value must not be empty")
			assert.Contains(t,
				[]strictness.Class{strictness.ClassConfig, strictness.ClassMigration},
				kind.StrictnessClass(),
				"every warning kind must bucket into a fatal class")
			assert.NotEmpty(t, kind.FixIt(), "every warning kind must name its fix")
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

// --- RecordWarningsTo / RecordWarnings --------------------------------------
//
// RecordWarningsTo is how `ctxloom run`, `ctxloom mcp`, and the GetConfig-based
// command entrypoints surface the errors Load downgraded to warnings
// (CLAUDE.md fault tolerance) — without it a corrupted yaml silently
// launches an empty-context session. RecordWarnings is the same recording loop
// against the ambient clidiag sink (used by the ACP session opener).

func TestRecordWarningsTo_EmitsPrefixedLinePerWarning(t *testing.T) {
	resetStrictness(t) // RecordWarningsTo records findings; keep them out of the shared collector
	var buf bytes.Buffer

	RecordWarningsTo(&buf, []Warning{
		{Kind: WarnKindParse, Text: "yaml is malformed: yaml: line 3: mapping values are not allowed"},
		{Kind: WarnKindValidate, Text: "profile \"dev\" failed schema validation"},
	})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	assert.Len(t, lines, 2, "one line per warning")
	for _, line := range lines {
		assert.True(t, strings.HasPrefix(line, "ctxloom: warning: "),
			"each warning must carry the project-standard prefix, got %q", line)
	}
	assert.Contains(t, buf.String(), "yaml is malformed")
	assert.Contains(t, buf.String(), "failed schema validation")

	// Each warning is ALSO recorded as a fatal finding so `ctxloom run`/`mcp`/`acp`
	// abort on a present-but-broken config (fail-loudly) instead of launching an
	// empty-context session — the whole point of surfacing them here.
	findings := strictness.All()
	require.Len(t, findings, 2, "each config warning records a fatal startup finding")
	assert.Equal(t, strictness.ClassConfig, findings[0].Class, "a parse warning is config-class")
	assert.Equal(t, strictness.ClassConfig, findings[1].Class, "a validate warning is config-class")
	assert.NotEmpty(t, findings[0].FixIt, "the finding carries a fix-it hint")
	assert.Contains(t, findings[0].Message, "yaml is malformed", "the finding echoes the warning text")
}

// An unknown config key is the silent-no-op trap: ctxloom drops the key and
// launches with a context the user never asked for. In strict mode it must
// abort startup with a config-class finding that NAMES the key and carries a
// fix-it — a crash names itself, a no-op does not.
func TestRecordWarningsTo_UnknownKeyIsFatalAndNamesTheKey(t *testing.T) {
	resetStrictness(t)
	var buf bytes.Buffer
	mark := strictness.Checkpoint()

	RecordWarningsTo(&buf, []Warning{{
		Kind: WarnKindUnknownKey,
		Text: "unknown key `profiles.defaults` in /p/.ctxloom/yaml: ctxloom does not know it, so it is IGNORED — `profiles.defaults` was RETIRED",
	}})

	assert.Contains(t, buf.String(), "profiles.defaults", "the warning line names the offending key")

	findings := strictness.Since(mark)
	require.Len(t, findings, 1, "an unknown key is a fatal startup finding, not a silent drop")
	assert.Equal(t, strictness.ClassConfig, findings[0].Class, "an unknown key is config-class")
	assert.Contains(t, findings[0].Message, "profiles.defaults")
	assert.Contains(t, findings[0].FixIt, "yaml", "the finding tells the user where to make the edit")
}

// --degraded / CTXLOOM_DEGRADED=1 is the established escape hatch: the same
// unknown key still WARNS (no diagnostic is ever lost) but records no finding, so
// startup proceeds. An unknown key must not be able to wedge a user out of their
// own tool.
func TestRecordWarningsTo_UnknownKeyDegradesToWarning(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	var buf bytes.Buffer

	RecordWarningsTo(&buf, []Warning{{
		Kind: WarnKindUnknownKey,
		Text: "unknown key `profilez` in /p/.ctxloom/yaml: ctxloom does not know it, so it is IGNORED",
	}})

	assert.Contains(t, buf.String(), "profilez", "degraded mode still prints the warning")
	assert.Empty(t, strictness.Actionable(strictness.All()),
		"degraded mode still COLLECTS the finding; what must be empty is what the gate acts on")
}

func TestRecordWarningsTo_NoWarningsIsSilent(t *testing.T) {
	var buf bytes.Buffer
	RecordWarningsTo(&buf, nil)
	assert.Empty(t, buf.String())
}

// RecordWarningsTo is called from multiple startup sites, one of which fires
// on every one of ~80 GetConfig()/GetConfigForUpdate() call sites in cli — and
// Load is MEMOIZED, so each of those calls hands back the same warnings
// again. Recording with strictness.Record, which has no dedup, would therefore
// turn ONE broken yaml into N identical fatal findings.
//
// The right dedup is the one RecordOnce documents and this file's window
// semantics require: scoped to the recording goroutine's CURRENT checkpoint
// window, never process-wide — a long-lived server that refused a session over
// this finding must see it again in the next window, or the retry opens
// silently on the same broken
func TestRecordWarningsTo_OneProblemRecordsOneFindingPerWindow(t *testing.T) {
	resetStrictness(t)
	warnings := []Warning{{Kind: WarnKindParse, Text: "yaml: did not parse"}}

	mark := strictness.Checkpoint()
	var buf bytes.Buffer
	RecordWarningsTo(&buf, warnings)
	RecordWarningsTo(&buf, warnings)
	RecordWarningsTo(&buf, warnings)

	got := strictness.Since(mark)
	assert.Len(t, got, 1, "one broken config file is one finding, however many times the config is loaded")
}

// The mirror guard: a NEW checkpoint window must see the finding again. A
// process-wide dedup here would let a long-lived server refuse one session over
// a broken config and then open the next one silently on the same
func TestRecordWarningsTo_FindingRefiresInANewWindow(t *testing.T) {
	resetStrictness(t)
	warnings := []Warning{{Kind: WarnKindParse, Text: "yaml: did not parse"}}

	mark1 := strictness.Checkpoint()
	var buf bytes.Buffer
	RecordWarningsTo(&buf, warnings)
	require.Len(t, strictness.Since(mark1), 1)

	mark2 := strictness.Checkpoint()
	RecordWarningsTo(&buf, warnings)
	assert.Len(t, strictness.Since(mark2), 1,
		"the next session must be refused over the same unfixed config, not opened silently")
}

// Two DIFFERENT problems are two findings — the dedup must key on the message,
// not collapse the class.
func TestRecordWarningsTo_DistinctProblemsAreDistinctFindings(t *testing.T) {
	resetStrictness(t)
	mark := strictness.Checkpoint()
	var buf bytes.Buffer
	RecordWarningsTo(&buf, []Warning{
		{Kind: WarnKindParse, Text: "yaml: did not parse"},
		{Kind: WarnKindUnknownKey, Text: "yaml: unknown key foo"},
	})
	assert.Len(t, strictness.Since(mark), 2)
}

// TestRecordWarnings_UsesTheAmbientSink pins RecordWarnings's own half of the
// contract: it is a thin call against whatever clidiag.SetSink installed
// (the ACP session opener redirects it away from os.Stderr for the session's
// lifetime), not a hardcoded os.Stderr write — sharing recordWarnings with
// RecordWarningsTo must not lose that redirect.
func TestRecordWarnings_UsesTheAmbientSink(t *testing.T) {
	resetStrictness(t)
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	RecordWarnings([]Warning{{Kind: WarnKindParse, Text: "yaml: did not parse"}})

	assert.Contains(t, sink.String(), "yaml: did not parse", "RecordWarnings must write to the ambient sink")
	require.Len(t, strictness.All(), 1, "RecordWarnings must also record the fatal finding")
}
