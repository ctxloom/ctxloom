package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// TestRunBundleDistill_AllFilesFailedExitsNonZero pins U034-F02: per-file
// distill errors were appended to result.Errors and printed, but nothing
// converted a non-empty result.Errors into a non-nil error for the command —
// `ctxloom bundle distill` over files that ALL fail to parse exited 0.
func TestRunBundleDistill_AllFilesFailedExitsNonZero(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	_ = config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	t.Chdir(root)

	broken := filepath.Join(root, "broken.yaml")
	require.NoError(t, os.WriteFile(broken, []byte(":::not valid yaml:::\n\tbad indent\n"), 0o644))

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	err := runBundleDistill(cmd, []string{broken})
	require.Error(t, err, "bundle distill must not exit 0 when every input file failed and nothing was written")
	assert.Contains(t, err.Error(), "1 of 1")
}

// expandDistillFiles resolves glob patterns and literal paths, warns on
// no-match, and errors only when nothing resolves.
func TestExpandDistillFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b.yaml")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, []byte("name: x\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", f, err)
		}
	}

	t.Run("glob expands to matches", func(t *testing.T) {
		got, err := expandDistillFiles([]string{filepath.Join(dir, "*.yaml")})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %v, want 2 files", got)
		}
	})

	t.Run("literal path passes through", func(t *testing.T) {
		got, err := expandDistillFiles([]string{a})
		if err != nil || len(got) != 1 || got[0] != a {
			t.Fatalf("got %v, err %v; want [%s]", got, err, a)
		}
	})

	t.Run("no match anywhere errors", func(t *testing.T) {
		if _, err := expandDistillFiles([]string{filepath.Join(dir, "nope-*.yaml")}); err == nil {
			t.Error("expected an error when no files resolve")
		}
	})

	t.Run("missing literal is warned but a present sibling still resolves", func(t *testing.T) {
		got, err := expandDistillFiles([]string{filepath.Join(dir, "ghost.yaml"), b})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 1 || got[0] != b {
			t.Fatalf("got %v, want [%s]", got, b)
		}
	})
}

// countDistillItems and printDistillItems replaced the old renderDistillItems
// (which printed AND counted in one pass) once bundle distill started
// buffering its result for emit(). Pin that the split kept both halves
// correct: printing is now purely a function of the items (no counting side
// effect), and counting has no output side effect.
func TestCountDistillItems_TalliesByStatus(t *testing.T) {
	items := []operations.DistillBundleItem{
		{Status: operations.DistillStatusDistilled},
		{Status: operations.DistillStatusPlanned},
		{Status: operations.DistillStatusSkipped},
		{Status: operations.DistillStatusSkipped},
	}
	processed, skipped := countDistillItems(items)
	assert.Equal(t, 2, processed)
	assert.Equal(t, 2, skipped)
}

func TestPrintDistillItems_OneLinePerItem(t *testing.T) {
	var buf bytes.Buffer
	printDistillItems(iox.NewErrWriter(&buf), []operations.DistillBundleItem{
		{Kind: operations.ItemKindFragment, Name: "a", Status: operations.DistillStatusDistilled, ModelID: "m1"},
		{Kind: operations.ItemKindCommand, Name: "b", Status: operations.DistillStatusSkipped, Reason: "unchanged"},
		{Kind: operations.ItemKindFragment, Name: "c", Status: operations.DistillStatusPlanned},
	})
	out := buf.String()
	assert.Contains(t, out, "Distilled fragment: a (m1)")
	assert.Contains(t, out, "Skipping command b (unchanged)")
	assert.Contains(t, out, "Would distill fragment: c")
}

func TestPrintDistillSummary_DryRunReportsWouldDistillCount(t *testing.T) {
	var buf bytes.Buffer
	printDistillSummary(iox.NewErrWriter(&buf), 3, 0, 0, true)
	assert.Contains(t, buf.String(), "Dry run: would distill 3 items")
}

func TestPrintDistillSummary_NoItemsReportsNothingToDistill(t *testing.T) {
	var buf bytes.Buffer
	printDistillSummary(iox.NewErrWriter(&buf), 0, 0, 0, false)
	assert.Contains(t, buf.String(), "No items to distill.")
}

// The sibling context is part of the message sent to the distiller, so its
// ordering is an INPUT to the model: two runs over the same bundle must build
// byte-identical context or distillation is nondeterministic run-to-run.
func TestBuildSiblingContext_IsDeterministic(t *testing.T) {
	b := &bundles.Bundle{
		Name:        "kitchen",
		Description: "a bundle with several siblings",
		Version:     "1.2.3",
		Tags:        []string{"alpha", "beta"},
		Fragments: map[string]bundles.BundleFragment{
			"delta":   {Content: "delta body"},
			"alpha":   {Content: "alpha body"},
			"charlie": {Content: "charlie body"},
			"bravo":   {Content: "bravo body"},
			"echo":    {Content: "echo body"},
		},
		Commands: map[string]bundles.BundleCommand{
			"zulu":    {Description: "zulu desc"},
			"yankee":  {Description: "yankee desc"},
			"xray":    {Description: "xray desc"},
			"whiskey": {Description: "whiskey desc"},
			"victor":  {Description: "victor desc"},
		},
	}

	first := buildSiblingContext(b, "fragments/alpha")
	for i := 0; i < 50; i++ {
		assert.Equal(t, first, buildSiblingContext(b, "fragments/alpha"),
			"sibling context must not depend on Go map iteration order (run %d)", i)
	}

	// And the order is the stable one a reader can predict, not merely repeatable.
	assert.Less(t, strings.Index(first, "- bravo:"), strings.Index(first, "- charlie:"))
	assert.Less(t, strings.Index(first, "- charlie:"), strings.Index(first, "- delta:"))
	assert.Less(t, strings.Index(first, "- victor:"), strings.Index(first, "- whiskey:"))
	assert.Less(t, strings.Index(first, "- whiskey:"), strings.Index(first, "- xray:"))
}

// The "is this the item being distilled?" test and the sibling-listing guard
// both key off an item's REF PREFIX, and item_kind.go's itemRefPrefix is the
// one producer of that prefix. Pin the values and the exclusion behaviour for
// both kinds, so routing the distill helpers through itemRefPrefix cannot
// change what is excluded.
func TestSiblingContext_ExcludesTheDistillingItemByRefPrefix(t *testing.T) {
	assert.Equal(t, "fragments/", itemRefPrefix(ItemTypeFragment))
	assert.Equal(t, "commands/", itemRefPrefix(ItemTypeCommand))

	b := &bundles.Bundle{
		Description: "two of each",
		Fragments: map[string]bundles.BundleFragment{
			"keep-frag": {Content: "keep"},
			"drop-frag": {Content: "drop"},
		},
		Commands: map[string]bundles.BundleCommand{
			"keep-cmd": {Description: "keep"},
			"drop-cmd": {Description: "drop"},
		},
	}

	frag := buildSiblingContext(b, itemRefPrefix(ItemTypeFragment)+"drop-frag")
	assert.Contains(t, frag, "- keep-frag:")
	assert.NotContains(t, frag, "- drop-frag:")
	assert.Contains(t, frag, "- drop-cmd:", "a fragment exclusion must not hide a same-named command")

	cmd := buildSiblingContext(b, itemRefPrefix(ItemTypeCommand)+"drop-cmd")
	assert.Contains(t, cmd, "- keep-cmd:")
	assert.NotContains(t, cmd, "- drop-cmd:")
	assert.Contains(t, cmd, "- drop-frag:")
}

// errWriteRefused is the failure the shared failingWriter (session_watch_test.go)
// is armed with here: a renderer that ignores its writer's errors is
// indistinguishable from one that succeeded.
var errWriteRefused = errors.New("write refused")

// `bundle distill`'s text renderer is the only one in this unit that wrote
// through bare fmt.Fprintf and returned nil unconditionally: a broken stdout
// (closed pipe, full disk) produced a silent success. And its per-file load
// failures went to the process's os.Stderr rather than the writer cobra was
// given, so they were unassertable and unredirectable.
func TestRunBundleDistill_TextPathReportsWriteFailuresAndUsesCommandWriters(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	_ = config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	t.Chdir(root)

	broken := filepath.Join(root, "broken.yaml")
	require.NoError(t, os.WriteFile(broken, []byte(":::not valid yaml:::\n\tbad indent\n"), 0o644))

	t.Run("per-file errors go to the command's error writer", func(t *testing.T) {
		var out, errBuf bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		cmd.SetErr(&errBuf)
		require.Error(t, runBundleDistill(cmd, []string{broken}))
		assert.Contains(t, errBuf.String(), "broken.yaml", "the per-file failure must reach cmd.ErrOrStderr()")
	})

	t.Run("a failing stdout is reported, not swallowed", func(t *testing.T) {
		good := filepath.Join(root, "good.yaml")
		require.NoError(t, os.WriteFile(good, []byte("name: good\ndescription: a bundle\n"), 0o644))

		cmd := &cobra.Command{}
		cmd.SetOut(&failingWriter{err: errWriteRefused})
		cmd.SetErr(io.Discard)
		err := runBundleDistill(cmd, []string{good})
		require.Error(t, err, "a write failure on the text path must not be reported as success")
		assert.ErrorIs(t, err, errWriteRefused)
	})
}
