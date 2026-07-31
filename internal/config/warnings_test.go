package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
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
