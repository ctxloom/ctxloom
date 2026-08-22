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

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// TestRunBundleDistill_AllFilesFailedExitsNonZero pins that per-file
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
		agentProject(t, "version: 6\n")
		cfg, err := GetConfig()
		require.NoError(t, err)

		got, err := loadDistillPrompt(cfg)
		require.NoError(t, err, "an absent prompt is not a refusal")
		assert.Equal(t, defaultDistillPrompt, got)
		assert.NotEmpty(t, got, "a distill run must never be handed an empty prompt")
	})

	t.Run("a bundle-provided distill command wins", func(t *testing.T) {
		agentProject(t, "version: 6\n")
		cfg, err := GetConfig()
		require.NoError(t, err)
		seedDistillCommand(t, cfg)

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
func seedDistillCommand(t *testing.T, cfg *config.Config) {
	t.Helper()
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "distiller"})
	require.NoError(t, err)
	_, err = operations.AddItem(context.Background(), cfg, operations.AddItemRequest{
		Kind:    operations.ItemKindCommand,
		Bundle:  "distiller",
		Name:    "distill",
		Content: distillCommandBody,
	})
	require.NoError(t, err)
}

// withholdDistillCommand puts the seeded `distill` command into a genuinely
// withheld state through the SAME operation `ctxloom review`'s reject arm uses
// (operations.SetBlacklist, via reviewApplier), rather than by injecting a
// denying authorizer. The point of the test is that the real trust gate's real
// verdict is honoured, so the real verdict is what it produces.
func withholdDistillCommand(t *testing.T, cfg *config.Config) {
	t.Helper()
	// An unsigned rejection lands in the USER countersignature store
	// (~/.ctxloom/approvals), which is process-wide state shared with the
	// developer's real machine and with every later test in this package.
	// isolatedHome must already have redirected it.
	require.NotEqual(t, "", os.Getenv("HOME"))
	_, err := operations.SetBlacklist(cfg, operations.SetBlacklistRequest{Ref: "distiller#commands/distill"})
	require.NoError(t, err)

	// Precondition, asserted rather than assumed: the gate really does withhold
	// it now. Without this the test below could pass for the wrong reason.
	_, gerr := operations.GetCommand(context.Background(), cfg, operations.GetCommandRequest{Name: "distill"})
	require.ErrorIs(t, gerr, errs.ErrCommandWithheld, "fixture precondition: the trust gate must withhold the distill command")
}

// isolatedHome points $HOME at a fresh temp dir for one test. The USER
// countersignature store is resolved from $HOME (paths.HomeApprovalsPath), so a
// test that records a rejection writes into the developer's real ~/.ctxloom and
// leaks that decision into every later test in the package unless HOME moves
// first.
func isolatedHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// distillProjectYAML is a project whose fast role resolves, so a distiller is
// actually constructed — without a resolvable label newLLMDistillerForLabel
// returns early and the prompt is never resolved at all.
const distillProjectYAML = "version: 6\nllm:\n  configs:\n    fast: { type: claude-code, model: haiku }\n  defaults:\n    fast: fast\n"

// TestBundleDistill_WithheldPromptRefuses is the decisive assertion for the
// swallow. A withheld `distill` prompt means the trust gate DECLINED to supply
// it; substituting ctxloom's default converts a withheld item into a used one
// and hands back a distillation the user believes came from their own prompt —
// exit 0, plausible output, wrong provenance.
//
// It asserts EFFECTS, not a returned error: the bundle file on disk is
// BYTE-IDENTICAL afterwards (nothing was distilled with the default), stdout
// never claims a distillation, the exit code is the refusal 2, and the message
// names the withheld item and the command that resolves it. Restoring the
// swallow leaves the run proceeding on the default and exiting 0, so these
// assertions fail.
func TestBundleDistill_WithheldPromptRefuses(t *testing.T) {
	isolatedHome(t)
	root := agentProject(t, distillProjectYAML)
	cfg, err := GetConfig()
	require.NoError(t, err)
	seedDistillCommand(t, cfg)
	withholdDistillCommand(t, cfg)

	target := filepath.Join(root, "target.yaml")
	const targetBody = "name: target\ndescription: a bundle\nfragments:\n  f:\n    content: some prose worth compressing, at length, repeatedly.\n"
	require.NoError(t, os.WriteFile(target, []byte(targetBody), 0o644))

	var out, errBuf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	runErr := runBundleDistill(cmd, []string{target})

	// EFFECT 1: the distill pass never BEGAN. "Processing: <file>" is printed
	// per input file the moment the run starts working through them, so its
	// absence is the observable difference between refusing and proceeding on
	// the default prompt — and it does not depend on an LLM being reachable,
	// which in a unit test it is not.
	assert.NotContains(t, out.String(), "Processing:", "the run must stop before it starts processing files")
	assert.NotContains(t, out.String(), "Distilled", "no item may be reported distilled when the configured prompt was withheld")

	// EFFECT 2: the bundle on disk is untouched. Weaker than EFFECT 1 on a test
	// host (no engine resolves, so a proceeding run would leave the content raw
	// anyway) but it is the property that actually matters in production, and
	// it must hold on every path.
	after, rerr := os.ReadFile(target)
	require.NoError(t, rerr)
	assert.Equal(t, targetBody, string(after), "a withheld prompt must leave the bundle exactly as it was, not distill it with ctxloom's default")

	// EFFECT 3: the refusal is exit 2 — completed, deliberately did not do it —
	// not 0 (indistinguishable from a clean run) and not 1 (a fault that isn't).
	var exitErr *ExitError
	require.ErrorAs(t, runErr, &exitErr, "a withheld prompt must refuse, not succeed (stderr: %s)", errBuf.String())
	assert.Equal(t, exitCodeRefused, exitErr.Code)

	// EFFECT 4: the message names the withheld item and the way out.
	said := errBuf.String()
	assert.Contains(t, said, "distill", "the refusal must name the withheld item")
	assert.Contains(t, said, "withheld", "the refusal must say WHY")
	assert.Contains(t, said, "ctxloom review", "the refusal must name the command that resolves it")
}

// TestDistillerForEdit_WithheldPromptRefusesUnlessNoDistill covers the OTHER
// frontend that resolves a distill prompt: an item edit re-distills by default,
// so a withheld prompt has to stop it for the same reason it stops
// `bundle distill` — otherwise the edit is silently distilled with ctxloom's
// default and written back. --no-distill resolves no prompt at all and must
// stay unaffected.
func TestDistillerForEdit_WithheldPromptRefusesUnlessNoDistill(t *testing.T) {
	isolatedHome(t)
	agentProject(t, distillProjectYAML)
	cfg, err := GetConfig()
	require.NoError(t, err)
	seedDistillCommand(t, cfg)
	withholdDistillCommand(t, cfg)

	d, err := distillerForEdit(cfg, false)
	require.ErrorIs(t, err, errs.ErrCommandWithheld, "an edit that will distill must refuse on a withheld prompt")
	assert.Nil(t, d, "a refused edit gets no distiller to fall back on")

	skipped, err := distillerForEdit(cfg, true)
	require.NoError(t, err, "--no-distill resolves no prompt, so a withheld one cannot refuse it")
	assert.Nil(t, skipped)
}

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
	seedDistillCommand(t, cfg)

	d, err := newLLMDistillerForLabel(cfg, "fast")
	require.NoError(t, err, "an admitted prompt is not a refusal")
	ld, ok := d.(*llmDistiller)
	require.True(t, ok)
	assert.Equal(t, distillCommandBody, ld.prompt, "the configured prompt is what gets used")
}
