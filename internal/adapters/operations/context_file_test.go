package operations

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/delivery/deliverytest"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestResolveContextFile_StaysInsideTheTarget (materialize test 14):
// relative, nested and absolute paths inside the target resolve to a slash
// path relative to it; anything escaping it is refused, typed.
func TestResolveContextFile_StaysInsideTheTarget(t *testing.T) {
	target := filepath.FromSlash("/work/t")
	for dest, want := range map[string]string{
		"":                                 "",
		"AGENTS.md":                        "AGENTS.md",
		"docs/AGENTS.md":                   "docs/AGENTS.md",
		"./docs/../AGENTS.md":              "AGENTS.md",
		filepath.Join(target, "x", "y.md"): "x/y.md",
	} {
		got, err := ResolveContextFile(target, dest)
		require.NoError(t, err, dest)
		require.Equal(t, want, got, dest)
	}
	for _, dest := range []string{"../x", "docs/../../x", filepath.FromSlash("/elsewhere/x.md"), ".", target} {
		_, err := ResolveContextFile(target, dest)
		require.ErrorIs(t, err, ErrContextFileOutsideTarget, dest)
	}
}

func deliverContextTo(t *testing.T, fs afero.Fs, kind engine.Engine, body, file string) {
	t.Helper()
	p := atRestPlacement(placeDir, kind.Root().Name, []present.Kind{present.Context})
	p.ContextFile = file
	_, _, err := Deliver(context.Background(), safefs.NewMem(fs), kind, everyKindPackage(t, body), delivery.Loadout{}, p)
	require.NoError(t, err)
}

// TestDeliver_ContextIntoANamedFile (materialize tests 15 and 16): the
// user's own text stays an exact prefix and a re-run leaves one section;
// switching the destination releases the old section, and a file ctxloom
// created is deleted.
func TestDeliver_ContextIntoANamedFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	kind := lookupEngine(t, "claude-code")
	mine := "# Agents\n\nthe user's own words\n"
	testsupport.WriteFileString(t, fs, filepath.Join(placeDir, "docs", "AGENTS.md"), mine, 0o644)

	deliverContextTo(t, fs, kind, "SECTION-BODY", "docs/AGENTS.md")
	deliverContextTo(t, fs, kind, "SECTION-BODY", "docs/AGENTS.md")
	got := placedString(t, fs, "docs/AGENTS.md")
	require.True(t, strings.HasPrefix(got, mine), "the user's text is an exact prefix")
	require.Equal(t, 1, strings.Count(got, "SECTION-BODY"), "a re-run is one section")
	require.NotContains(t, deliverytest.RelativeFiles(fs, placeDir), claude.ContextFileName, "the engine's own file is not written")

	deliverContextTo(t, fs, kind, "SECTION-BODY", "NEW.md")
	require.Equal(t, mine, placedString(t, fs, "docs/AGENTS.md"), "the old section left, byte for byte")
	require.Contains(t, placedString(t, fs, "NEW.md"), "SECTION-BODY")

	deliverContextTo(t, fs, kind, "SECTION-BODY", "")
	require.NotContains(t, deliverytest.RelativeFiles(fs, placeDir), "NEW.md", "a file ctxloom created is deleted when the section leaves")
}

// TestDeliver_AnEmptyContextFileIsTheEnginesOwn (materialize test 19): the
// expected name is each engine package's constant.
func TestDeliver_AnEmptyContextFileIsTheEnginesOwn(t *testing.T) {
	for name, want := range map[string]string{"claude-code": claude.ContextFileName, "mock": mock.ContextFileName} {
		fs := afero.NewMemMapFs()
		deliverContextTo(t, fs, lookupEngine(t, name), "BODY", "")
		require.Equal(t, []string{want}, deliverytest.RelativeFiles(fs, placeDir), name)
	}
}
