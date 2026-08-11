package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// failOpenFs wraps an afero.Fs and fails Open/OpenFile for one path with a
// non-IsNotExist error, modeling an existing-but-unreadable config (EACCES, a
// directory in its place).
type failOpenFs struct {
	afero.Fs
	path string
}

func (f failOpenFs) Open(name string) (afero.File, error) {
	if name == f.path {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrPermission}
	}
	return f.Fs.Open(name)
}

func (f failOpenFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if name == f.path {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrPermission}
	}
	return f.Fs.OpenFile(name, flag, perm)
}

// An existing-but-unreadable config degrades with a kind-tagged read warning —
// the kind is what the strict startup gate aborts on.
func TestLoad_UnreadableConfigTaggedRead(t *testing.T) {
	base := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, base.MkdirAll(appDir, 0755))
	cfgPath := paths.ConfigPath(appDir)
	require.NoError(t, afero.WriteFile(base, cfgPath, []byte("llm: {}\n"), 0644))

	cfg, err := Load(WithFS(failOpenFs{Fs: base, path: cfgPath}), WithAppDir(appDir))
	require.NoError(t, err, "unreadable config must not hard-error the load itself")
	require.Len(t, cfg.warnings, 1)
	assert.Equal(t, WarnKindRead, cfg.warnings[0].Kind)
	assert.Contains(t, cfg.warnings[0].Text, "failed to read config")
}

// Broken YAML is tagged parse (plus the validator's validate warning), so the
// gate can distinguish a broken file from an absent one.
func TestLoad_BrokenYAMLTaggedParse(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("llm: [unclosed\n"), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)
	require.NotEmpty(t, cfg.warnings)
	kinds := make(map[WarningKind]bool)
	for _, w := range cfg.warnings {
		kinds[w.Kind] = true
	}
	assert.True(t, kinds[WarnKindParse], "broken YAML must carry a parse-kind warning; got %v", cfg.warnings)
}

// An absent config file is fine: no warnings, no findings — strict mode only
// bites on present-but-broken files.
func TestLoad_AbsentConfigNoWarnings(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)
	assert.Empty(t, cfg.warnings)
}

// A lossy schema migration (the dropped compaction model at
// config_migrate.go's v2→v3 step) surfaces as a migration-lossy warning naming
// the key to fix, instead of a loose stderr line the gate cannot see.
func TestLoad_LossyMigrationTaggedMigrationLossy(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))
	// compaction.model with no compaction.llm and no primary label: the model
	// has no label to attach to and is dropped by migrateLLMv3.
	lossy := "llm:\n  compaction:\n    model: haiku\n"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(lossy), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)

	var lossyWarnings []Warning
	for _, w := range cfg.warnings {
		if w.Kind == WarnKindMigrationLossy {
			lossyWarnings = append(lossyWarnings, w)
		}
	}
	require.Len(t, lossyWarnings, 1, "the dropped compaction model must be tagged migration-lossy; warnings: %v", cfg.warnings)
	assert.Contains(t, lossyWarnings[0].Text, "dropped compaction model")
	assert.Contains(t, lossyWarnings[0].Text, "llm.defaults.fast", "the message must name the key to fix")

	// The collector drains per load: a subsequent clean load carries nothing over.
	fs2 := afero.NewMemMapFs()
	require.NoError(t, fs2.MkdirAll(appDir, 0755))
	cfg2, err := Load(WithFS(fs2), WithAppDir(appDir))
	require.NoError(t, err)
	assert.Empty(t, cfg2.warnings, "migration warnings must not leak into later loads")
}

// --- the contract WarningKind's own doc states -----------------------------

// allWarningKinds is every kind config.Load can attach to a Warning. A kind
// missing from here is a kind nothing below checks, so keep it exhaustive.
var allWarningKinds = []WarningKind{
	WarnKindRead,
	WarnKindParse,
	WarnKindValidate,
	WarnKindUnknownKey,
	WarnKindMigrationLossy,
}

// The doc on WarningKind promises that every kind is fatal-class in strict
// mode and the fail-loudly gate depends on it: a kind that mapped to no fatal
// class would degrade silently on exactly the startup paths that exist to
// refuse a broken config. Each must also carry an actionable fix-it, since the
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
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "cannot resolve this test's own source path")
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "warnings.go"))
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
// command entrypoints surface the errors config.Load downgraded to warnings
// (CLAUDE.md fault tolerance) — without it a corrupted config.yaml silently
// launches an empty-context session. RecordWarnings is the same recording loop
// against the ambient clidiag sink (used by the ACP session opener).

func TestRecordWarningsTo_EmitsPrefixedLinePerWarning(t *testing.T) {
	resetStrictness(t) // RecordWarningsTo records findings; keep them out of the shared collector
	var buf bytes.Buffer

	RecordWarningsTo(&buf, []Warning{
		{Kind: WarnKindParse, Text: "config.yaml is malformed: yaml: line 3: mapping values are not allowed"},
		{Kind: WarnKindValidate, Text: "profile \"dev\" failed schema validation"},
	})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	assert.Len(t, lines, 2, "one line per warning")
	for _, line := range lines {
		assert.True(t, strings.HasPrefix(line, "ctxloom: warning: "),
			"each warning must carry the project-standard prefix, got %q", line)
	}
	assert.Contains(t, buf.String(), "config.yaml is malformed")
	assert.Contains(t, buf.String(), "failed schema validation")

	// Each warning is ALSO recorded as a fatal finding so `ctxloom run`/`mcp`/`acp`
	// abort on a present-but-broken config (fail-loudly) instead of launching an
	// empty-context session — the whole point of surfacing them here.
	findings := strictness.All()
	require.Len(t, findings, 2, "each config warning records a fatal startup finding")
	assert.Equal(t, strictness.ClassConfig, findings[0].Class, "a parse warning is config-class")
	assert.Equal(t, strictness.ClassConfig, findings[1].Class, "a validate warning is config-class")
	assert.NotEmpty(t, findings[0].FixIt, "the finding carries a fix-it hint")
	assert.Contains(t, findings[0].Message, "config.yaml is malformed", "the finding echoes the warning text")
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
		Text: "unknown key `profiles.defaults` in /p/.ctxloom/config.yaml: ctxloom does not know it, so it is IGNORED — `profiles.defaults` was RETIRED",
	}})

	assert.Contains(t, buf.String(), "profiles.defaults", "the warning line names the offending key")

	findings := strictness.Since(mark)
	require.Len(t, findings, 1, "an unknown key is a fatal startup finding, not a silent drop")
	assert.Equal(t, strictness.ClassConfig, findings[0].Class, "an unknown key is config-class")
	assert.Contains(t, findings[0].Message, "profiles.defaults")
	assert.Contains(t, findings[0].FixIt, "config.yaml", "the finding tells the user where to make the edit")
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
		Text: "unknown key `profilez` in /p/.ctxloom/config.yaml: ctxloom does not know it, so it is IGNORED",
	}})

	assert.Contains(t, buf.String(), "profilez", "degraded mode still prints the warning")
	assert.Empty(t, strictness.All(), "degraded mode records no fatal finding")
}

func TestRecordWarningsTo_NoWarningsIsSilent(t *testing.T) {
	var buf bytes.Buffer
	RecordWarningsTo(&buf, nil)
	assert.Empty(t, buf.String())
}

// RecordWarningsTo is called from multiple startup sites, one of which fires
// on every one of ~80 GetConfig()/GetConfigForUpdate() call sites in cli — and
// config.Load is MEMOIZED, so each of those calls hands back the same warnings
// again. Recording with strictness.Record, which has no dedup, would therefore
// turn ONE broken config.yaml into N identical fatal findings.
//
// The right dedup is the one RecordOnce documents and this file's window
// semantics require: scoped to the recording goroutine's CURRENT checkpoint
// window, never process-wide — a long-lived server that refused a session over
// this finding must see it again in the next window, or the retry opens
// silently on the same broken config.
func TestRecordWarningsTo_OneProblemRecordsOneFindingPerWindow(t *testing.T) {
	resetStrictness(t)
	warnings := []Warning{{Kind: WarnKindParse, Text: "config.yaml: did not parse"}}

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
// a broken config and then open the next one silently on the same config.
func TestRecordWarningsTo_FindingRefiresInANewWindow(t *testing.T) {
	resetStrictness(t)
	warnings := []Warning{{Kind: WarnKindParse, Text: "config.yaml: did not parse"}}

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
		{Kind: WarnKindParse, Text: "config.yaml: did not parse"},
		{Kind: WarnKindUnknownKey, Text: "config.yaml: unknown key foo"},
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

	RecordWarnings([]Warning{{Kind: WarnKindParse, Text: "config.yaml: did not parse"}})

	assert.Contains(t, sink.String(), "config.yaml: did not parse", "RecordWarnings must write to the ambient sink")
	require.Len(t, strictness.All(), 1, "RecordWarnings must also record the fatal finding")
}
