package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
)

// TestRunBundleDistill_AllFilesFailedExitsNonZero pins that per-file
// distill errors were appended to result.Errors and printed, but nothing
// converted a non-empty result.Errors into a non-nil error for the command —
// `ctxloom bundle distill` over files that ALL fail to parse exited 0.
func TestRunBundleDistill_AllFilesFailedExitsNonZero(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	_ = config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	chdir(t, root)

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
		got, err := expandDistillFiles(afero.NewOsFs(), []string{filepath.Join(dir, "*.yaml")})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %v, want 2 files", got)
		}
	})

	t.Run("literal path passes through", func(t *testing.T) {
		got, err := expandDistillFiles(afero.NewOsFs(), []string{a})
		if err != nil || len(got) != 1 || got[0] != a {
			t.Fatalf("got %v, err %v; want [%s]", got, err, a)
		}
	})

	t.Run("no match anywhere errors", func(t *testing.T) {
		if _, err := expandDistillFiles(afero.NewOsFs(), []string{filepath.Join(dir, "nope-*.yaml")}); err == nil {
			t.Error("expected an error when no files resolve")
		}
	})

	t.Run("missing literal is warned but a present sibling still resolves", func(t *testing.T) {
		got, err := expandDistillFiles(afero.NewOsFs(), []string{filepath.Join(dir, "ghost.yaml"), b})
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
	printDistillItems(errwriter.New(&buf), []operations.DistillBundleItem{
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
	printDistillSummary(errwriter.New(&buf), 3, 0, 0, true)
	assert.Contains(t, buf.String(), "Dry run: would distill 3 items")
}

func TestPrintDistillSummary_NoItemsReportsNothingToDistill(t *testing.T) {
	var buf bytes.Buffer
	printDistillSummary(errwriter.New(&buf), 0, 0, 0, false)
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
			"delta": {
				ItemBody: bundles.ItemBody{
					Content: "delta body",
				},
			},
			"alpha": {
				ItemBody: bundles.ItemBody{
					Content: "alpha body",
				},
			},
			"charlie": {
				ItemBody: bundles.ItemBody{
					Content: "charlie body",
				},
			},
			"bravo": {
				ItemBody: bundles.ItemBody{
					Content: "bravo body",
				},
			},
			"echo": {
				ItemBody: bundles.ItemBody{
					Content: "echo body",
				},
			},
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
// both key off an item's SELECTOR, and ident.FormatSelector is its one
// renderer. Pin the values and the exclusion behaviour for both kinds, so
// routing the distill helpers through it cannot change what is excluded.
func TestSiblingContext_ExcludesTheDistillingItemByRefPrefix(t *testing.T) {
	assert.Equal(t, "fragments/x", ident.FormatSelector(itemKindOf(ItemTypeFragment), "x"))
	assert.Equal(t, "commands/x", ident.FormatSelector(itemKindOf(ItemTypeCommand), "x"))

	b := &bundles.Bundle{
		Description: "two of each",
		Fragments: map[string]bundles.BundleFragment{
			"keep-frag": {
				ItemBody: bundles.ItemBody{
					Content: "keep",
				},
			},
			"drop-frag": {
				ItemBody: bundles.ItemBody{
					Content: "drop",
				},
			},
		},
		Commands: map[string]bundles.BundleCommand{
			"keep-cmd": {Description: "keep"},
			"drop-cmd": {Description: "drop"},
		},
	}

	frag := buildSiblingContext(b, ident.FormatSelector(ident.KindFragment, "drop-frag"))
	assert.Contains(t, frag, "- keep-frag:")
	assert.NotContains(t, frag, "- drop-frag:")
	assert.Contains(t, frag, "- drop-cmd:", "a fragment exclusion must not hide a same-named command")

	cmd := buildSiblingContext(b, ident.FormatSelector(ident.KindPrompt, "drop-cmd"))
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
	chdir(t, root)

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

// loadDistillPrompt has exactly two legitimate answers and one refusal, and the
// bug this pins was that all three collapsed into "return the default": every
// error from operations.GetCommand — errs.ErrCommandWithheld included — fell
// through to defaultDistillPrompt, so a prompt the trust gate DECLINED to
// supply was silently replaced by ctxloom's own and the run reported success.
//
// The two legitimate answers are pinned here (absence → the embedded default;
// a configured command → that command). The refusal is
// TestBundleDistill_WithheldPromptRefuses, which asserts the EFFECT: nothing
// distilled, exit 2, and the item named.
func TestLoadDistillPrompt_AlwaysYieldsAUsablePrompt(t *testing.T) {
	require.NotEmpty(t, defaultDistillPrompt, "the embedded fallback is the whole reason absence needs no error")

	t.Run("no distill command anywhere falls back to the embedded default", func(t *testing.T) {
		agentProject(t, "schema_version: 7\n")
		cfg, err := GetConfig()
		require.NoError(t, err)

		got, err := loadDistillPrompt(cfg)
		require.NoError(t, err, "an absent prompt is not a refusal")
		assert.Equal(t, defaultDistillPrompt, got)
		assert.NotEmpty(t, got, "a distill run must never be handed an empty prompt")
	})

	t.Run("a bundle-provided distill command wins", func(t *testing.T) {
		agentProject(t, "schema_version: 7\n")
		cfg, err := GetConfig()
		require.NoError(t, err)
		cfg = seedDistillCommand(t, cfg)

		got, err := loadDistillPrompt(cfg)
		require.NoError(t, err)
		assert.Equal(t, distillCommandBody, got)
	})
}

// distillCommandBody is the project-configured distill prompt these tests seed.
// It is a distinctive string so an assertion can tell "the configured prompt"
// from "ctxloom's default" by BYTES, never by a proxy like non-emptiness — a
// silent substitution is precisely the failure being tested for.
const distillCommandBody = "COMPRESS THIS, project-specific rules apply."

// seedDistillCommand creates a bundle carrying a `distill` command in the
// project rooted at the current working directory.
// seedDistillCommand writes the distiller bundle and returns the generation
// that holds it: a write announces the next generation, and the caller reads
// from that one.
func seedDistillCommand(t *testing.T, cfg *config.Config) *config.Config {
	t.Helper()
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "distiller"})
	require.NoError(t, err)
	cfg = reloaded(t)
	_, err = operations.AddItem(context.Background(), cfg, operations.AddItemRequest{
		Kind:    operations.ItemKindCommand,
		Bundle:  "distiller",
		Name:    "distill",
		Content: distillCommandBody,
	})
	require.NoError(t, err)
	return reloaded(t)
}

// reloaded publishes the next generation — the one that holds what was just
// written — and returns its Config.
func reloaded(t *testing.T) *config.Config {
	t.Helper()
	_, err := App().Reload(context.Background())
	require.NoError(t, err)
	cfg, err := GetConfig()
	require.NoError(t, err)
	return cfg
}

// isolatedHome points $HOME at a fresh temp dir for one test, so anything the
// command resolves from $HOME reads and writes a throwaway ~/.ctxloom rather
// than the developer's real one, and nothing one test leaves there reaches a
// later test in the package.
func isolatedHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// distillProjectYAML is a project whose fast role resolves, so a distiller is
// actually constructed — without a resolvable label newLLMDistiller
// returns early and the prompt is never resolved at all.
const distillProjectYAML = "schema_version: 7\nllm:\n  configs:\n    fast: { type: claude-code, model: haiku }\n  defaults:\n    fast: fast\n"

// TestBundleDistill_TrustedPromptIsNotRefused is the negative control for the
// test above: the SAME project with the SAME configured prompt, only NOT
// withheld, must not be refused, and the distiller must carry the CONFIGURED
// bytes. Without it, a fix that refused whenever a `distill` command exists at
// all — or one that refused and then used the default anyway — would pass.
func TestBundleDistill_TrustedPromptIsNotRefused(t *testing.T) {
	isolatedHome(t)
	agentProject(t, distillProjectYAML)
	cfg, err := GetConfig()
	require.NoError(t, err)
	cfg = seedDistillCommand(t, cfg)

	d, err := newLLMDistiller(cfg, "fast")
	require.NoError(t, err, "an admitted prompt is not a refusal")
	require.NotNil(t, d)
	assert.Equal(t, distillCommandBody, d.prompt, "the configured prompt is what gets used")
}
