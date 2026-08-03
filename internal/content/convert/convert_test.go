package convert

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// newStore builds an empty tree store to convert INTO.
func newStore(t *testing.T) (*content.TreeStore, afero.Fs) {
	t.Helper()
	fsys := afero.NewMemMapFs()
	require.NoError(t, fsys.MkdirAll("/tree", 0o755))
	st, err := content.NewTreeStore(fsys, "/tree", content.Provenance{IsLocal: true})
	require.NoError(t, err)
	return st, fsys
}

func refNames(items []Item, kind trust.ItemKind) []string {
	var out []string
	for _, it := range items {
		if it.Ref.Kind == kind {
			out = append(out, it.Ref.Name)
		}
	}
	return out
}

// --- fragments -------------------------------------------------------------

func TestPlan_FragmentBecomesAMarkdownItemCarryingItsBodyAndMetadata(t *testing.T) {
	b := &bundles.Bundle{
		Name: "vault",
		Fragments: map[string]bundles.BundleFragment{
			"house-style": {
				Content:     "PROSE-BODY",
				Tags:        []string{"style"},
				Notes:       "NOTE-TEXT",
				ContentHash: "abc123",
			},
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)
	require.Equal(t, []string{"house-style"}, refNames(items, trust.KindFragment))

	var frag content.Fragment
	for _, it := range items {
		if f, ok := it.Surface.(content.Fragment); ok {
			frag = f
		}
	}
	assert.Equal(t, "PROSE-BODY", frag.Body, "the authored body must survive verbatim")
	assert.Equal(t, []string{"style"}, frag.Tags)
	assert.Equal(t, "NOTE-TEXT", frag.Notes)
	assert.Equal(t, "abc123", frag.ContentHash, "content_hash is bookkeeping and must be carried verbatim")
}

// A distilled fragment is TWO forms of ONE item, and converting it must produce
// both — losing the distilled form would silently downgrade every consumer on
// distilled context back to the verbose original.
func TestPlan_DistilledFragmentProducesBothForms(t *testing.T) {
	b := &bundles.Bundle{
		Name: "vault",
		Fragments: map[string]bundles.BundleFragment{
			"solid": {Content: "LONG", Distilled: "SHORT", DistilledBy: "model-x"},
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)

	forms := map[signing.Form]bool{}
	for _, it := range items {
		if it.Ref.Kind == trust.KindFragment {
			forms[it.Form] = true
		}
	}
	assert.True(t, forms[signing.FormRaw], "the raw form must be planned")
	assert.True(t, forms[signing.FormDistilled], "the distilled form must be planned, as a separate item")
}

func TestPlan_UndistilledFragmentProducesOnlyTheRawForm(t *testing.T) {
	b := &bundles.Bundle{
		Name:      "vault",
		Fragments: map[string]bundles.BundleFragment{"solid": {Content: "LONG"}},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)
	for _, it := range items {
		assert.NotEqual(t, signing.FormDistilled, it.Form,
			"a fragment with no distilled rewrite must not produce a distilled form")
	}
}

// --- commands --------------------------------------------------------------

// A command is a DIFFERENT kind from a fragment, not a spelling of it: it is
// user-invoked, it carries a description and per-engine export settings, and it
// is addressed under "prompts/". Converting one into a fragment would lose all
// three.
func TestPlan_CommandBecomesItsOwnKindUnderPrompts(t *testing.T) {
	b := &bundles.Bundle{
		Name: "vault",
		Commands: map[string]bundles.BundleCommand{
			"ship-it": {Content: "CMD-BODY", Description: "DESC"},
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)
	require.Equal(t, []string{"ship-it"}, refNames(items, trust.KindPrompt))
	assert.Equal(t, "prompts", trust.KindPrompt.Dir(),
		"a command's selector directory is prompts/, not commands/")

	for _, it := range items {
		if c, ok := it.Surface.(content.Command); ok {
			assert.Equal(t, "CMD-BODY", c.Body)
			assert.Equal(t, "DESC", c.Description)
		}
	}
}

// --- mcp -------------------------------------------------------------------

func TestPlan_MCPCarriesCommandArgsAndEnv(t *testing.T) {
	b := &bundles.Bundle{
		Name: "vault",
		MCP: map[string]bundles.BundleMCP{
			"ledger": {
				Command: "/usr/bin/ledger",
				Args:    []string{"--serve", "--port", "1"},
				Env:     map[string]string{"MODE": "readonly"},
				Notes:   "NOTE",
			},
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)
	require.Equal(t, []string{"ledger"}, refNames(items, trust.KindMCP))
	for _, it := range items {
		if m, ok := it.Surface.(content.MCP); ok {
			assert.Equal(t, "/usr/bin/ledger", m.Command)
			assert.Equal(t, []string{"--serve", "--port", "1"}, m.Args,
				"argument ORDER is meaning for an MCP server, not an incidental list")
			assert.Equal(t, map[string]string{"MODE": "readonly"}, m.Env)
			assert.Equal(t, "NOTE", m.Notes)
		}
	}
}

// --- hooks: bucketing AND order --------------------------------------------

// The load-bearing hook case. In a bundle document an event is an ORDERED LIST
// and hooks merge by pure append, so sequence is meaning. In the tree, hooks
// enumerate as SORTED FILENAMES. The converter is the only place that can
// reconcile those, and a converter that got it wrong would produce a tree that
// is byte-identical to a correct one while executing hooks in the wrong order.
func TestPlan_HookNamesSortIntoTheirDeclaredOrder(t *testing.T) {
	b := &bundles.Bundle{
		Name: "vault",
		Hooks: bundles.BundleHooks{
			// Deliberately named so ALPHABETICAL ORDER DISAGREES with declared
			// order: "stamp" is declared first but sorts after "audit".
			PostFileEdit: []bundles.BundleHook{
				{Type: "command", Command: "echo stamp"},
				{Type: "command", Command: "echo audit"},
			},
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)

	names := refNames(items, trust.KindHook)
	require.Len(t, names, 2)

	// Sorting the planned names must reproduce the DECLARED order, because
	// sorted order is what a directory walk yields.
	sorted := append([]string(nil), names...)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	assert.Equal(t, names, sorted,
		"planned hook names must already be in sorted order, or a directory walk will reorder them")

	// And the first-declared hook must be the one that sorts first.
	first := items[0]
	h, ok := first.Surface.(content.Hook)
	require.True(t, ok)
	assert.Equal(t, "echo stamp", h.Command,
		"the first hook a directory walk yields must be the first hook that was declared")
}

func TestPlan_HooksKeepTheirEventBuckets(t *testing.T) {
	b := &bundles.Bundle{
		Name: "vault",
		Hooks: bundles.BundleHooks{
			PostFileEdit: []bundles.BundleHook{{Type: "command", Command: "a"}},
			SessionStart: []bundles.BundleHook{{Type: "command", Command: "b"}},
			PreTool:      []bundles.BundleHook{{Type: "command", Command: "c"}},
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)

	byEvent := map[string]string{}
	for _, it := range items {
		if h, ok := it.Surface.(content.Hook); ok {
			byEvent[h.Event] = h.Command
		}
	}
	assert.Equal(t, map[string]string{
		"post_file_edit": "a",
		"session_start":  "b",
		"pre_tool":       "c",
	}, byEvent, "every declared event must survive as its own bucket")
}

// Every one of the six events must convert. An event silently dropped by a
// missing switch case is invisible: the tree still builds and the hooks simply
// never fire.
func TestPlan_EverySixHookEventsConverts(t *testing.T) {
	one := []bundles.BundleHook{{Type: "command", Command: "x"}}
	b := &bundles.Bundle{
		Name: "vault",
		Hooks: bundles.BundleHooks{
			PreTool: one, PostTool: one, SessionStart: one,
			SessionEnd: one, PreShell: one, PostFileEdit: one,
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)

	events := map[string]bool{}
	for _, it := range items {
		if h, ok := it.Surface.(content.Hook); ok {
			events[h.Event] = true
		}
	}
	for _, want := range []string{"pre_tool", "post_tool", "session_start", "session_end", "pre_shell", "post_file_edit"} {
		assert.True(t, events[want], "event %q was dropped by the converter", want)
	}
}

// --- profiles --------------------------------------------------------------

func TestPlan_ProfileIsConvertedToo(t *testing.T) {
	b := &bundles.Bundle{
		Name: "vault",
		Profiles: map[string]bundles.BundleProfile{
			"studio": {Description: "STUDIO-DESC"},
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)
	require.Equal(t, []string{"studio"}, refNames(items, content.KindProfile))
}

// --- skills ----------------------------------------------------------------

func TestPlan_SkillCarriesEveryPackageFileAndItsDeclaredMode(t *testing.T) {
	b := &bundles.Bundle{
		Name:   "vault",
		Skills: map[string]bundles.BundleSkill{"reviewer": {}},
	}
	items, err := Plan("vault", b, Options{
		SkillFiles: func(name string) ([]content.SkillFile, error) {
			return []content.SkillFile{
				{Path: "SKILL.md", Bytes: []byte("BODY"), Mode: content.ModeRegular},
				{Path: "scripts/run.sh", Bytes: []byte("#!/bin/sh\n"), Mode: content.ModeExecutable},
			}, nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"reviewer"}, refNames(items, trust.KindSkill))

	for _, it := range items {
		if s, ok := it.Surface.(content.Skill); ok {
			require.Len(t, s.Files, 2)
			modes := map[string]content.ComponentMode{}
			for _, f := range s.Files {
				modes[f.Path] = f.Mode
			}
			assert.Equal(t, content.ModeExecutable, modes["scripts/run.sh"],
				"the exec bit is DECLARED metadata and must survive conversion")
			assert.Equal(t, content.ModeRegular, modes["SKILL.md"])
		}
	}
}

// A bundle declaring a skill with no way to read its files must FAIL, not
// silently emit an empty package. An empty skill is this codebase's
// characteristic bug: it converts, it signs, it materializes, and the model
// gets nothing.
func TestPlan_SkillWithNoReadableFilesFailsLoudlyRatherThanConvertingToNothing(t *testing.T) {
	b := &bundles.Bundle{
		Name:   "vault",
		Skills: map[string]bundles.BundleSkill{"reviewer": {}},
	}
	_, err := Plan("vault", b, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reviewer")

	_, err = Plan("vault", b, Options{
		SkillFiles: func(string) ([]content.SkillFile, error) { return nil, nil },
	})
	require.Error(t, err, "a skill package with zero files must be refused, not written empty")
}

// --- end to end through the real Writer ------------------------------------

// The whole point of the converter is that what comes out is readable through
// the L0 surface. This drives Plan + Apply into a real TreeStore and then reads
// every item back through Bundle/Item/Form.
func TestConvert_RoundTripsEveryKindThroughTheL0Surface(t *testing.T) {
	st, _ := newStore(t)
	b := &bundles.Bundle{
		Name: "vault",
		Fragments: map[string]bundles.BundleFragment{
			"house-style": {Content: "FRAG-BODY"},
		},
		Commands: map[string]bundles.BundleCommand{
			"ship-it": {Content: "CMD-BODY", Description: "D"},
		},
		MCP: map[string]bundles.BundleMCP{
			"ledger": {Command: "/bin/ledger", Args: []string{"--serve"}},
		},
		Hooks: bundles.BundleHooks{
			PostFileEdit: []bundles.BundleHook{
				{Type: "command", Command: "echo stamp"},
				{Type: "command", Command: "echo audit"},
			},
		},
		Skills: map[string]bundles.BundleSkill{"reviewer": {}},
		Profiles: map[string]bundles.BundleProfile{
			"studio": {Description: "STUDIO"},
		},
	}
	opts := Options{SkillFiles: func(string) ([]content.SkillFile, error) {
		return []content.SkillFile{
			{Path: "SKILL.md", Bytes: []byte("SKILL-BODY"), Mode: content.ModeRegular},
			{Path: "scripts/run.sh", Bytes: []byte("#!/bin/sh\n"), Mode: content.ModeExecutable},
		}, nil
	}}

	ctx := context.Background()
	require.NoError(t, Convert(ctx, st, "vault", b, opts))

	bun, err := st.Open(ctx, "vault")
	require.NoError(t, err)

	refs, err := bun.Refs(ctx)
	require.NoError(t, err)

	got := map[trust.ItemKind][]string{}
	for _, r := range refs {
		got[r.Kind] = append(got[r.Kind], r.Name)
	}
	assert.Equal(t, []string{"house-style"}, got[trust.KindFragment])
	assert.Equal(t, []string{"ship-it"}, got[trust.KindPrompt])
	assert.Equal(t, []string{"ledger"}, got[trust.KindMCP])
	assert.Equal(t, []string{"reviewer"}, got[trust.KindSkill])
	assert.Equal(t, []string{"studio"}, got[content.KindProfile])
	require.Len(t, got[trust.KindHook], 2, "both hooks must be readable back")

	// ORDER, read back through the real enumeration — the assertion that a
	// name-keyed directory would silently break.
	first, err := bun.Item(ctx, refs[0])
	_ = first
	require.NoError(t, err)
	hookNames := got[trust.KindHook]
	it, err := bun.Item(ctx, trust.Ref{Bundle: "vault", Kind: trust.KindHook, Name: hookNames[0]})
	require.NoError(t, err)
	surf, err := it.Surface(ctx)
	require.NoError(t, err)
	h, ok := surf.(content.Hook)
	require.True(t, ok)
	assert.Equal(t, "echo stamp", h.Command,
		"reading the tree back must yield the FIRST-DECLARED hook first")
}

// Conversion must not invent files. Anything in the tree that the source bundle
// did not declare is a bug the digest would then faithfully sign.
func TestConvert_WritesNothingTheSourceBundleDidNotDeclare(t *testing.T) {
	st, _ := newStore(t)
	b := &bundles.Bundle{
		Name:      "vault",
		Fragments: map[string]bundles.BundleFragment{"only": {Content: "X"}},
	}
	ctx := context.Background()
	require.NoError(t, Convert(ctx, st, "vault", b, Options{}))

	bun, err := st.Open(ctx, "vault")
	require.NoError(t, err)
	files, err := bun.Files(ctx)
	require.NoError(t, err)
	for _, f := range files {
		assert.True(t, strings.HasPrefix(f, "fragments/"),
			"unexpected file %q in a bundle that declared only a fragment", f)
	}
}

// An EMPTY bundle must convert to an empty tree without error and without
// writing a single byte — and must not be mistaken for a successful conversion
// of a non-empty one.
func TestConvert_EmptyBundleWritesNothing(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	require.NoError(t, Convert(ctx, st, "vault", &bundles.Bundle{Name: "vault"}, Options{}))

	bun, err := st.Open(ctx, "vault")
	if err != nil {
		return // an absent bundle is an acceptable representation of "nothing was written"
	}
	refs, err := bun.Refs(ctx)
	require.NoError(t, err)
	assert.Empty(t, refs)
}

func TestPlan_NilBundleIsRefused(t *testing.T) {
	_, err := Plan("vault", nil, Options{})
	require.Error(t, err)
}

// Ten or more hooks in one event is where a naive ordinal reintroduces the very
// reordering it exists to prevent: "10-x" sorts BEFORE "2-x" under string
// comparison, so the padding width has to follow the count.
func TestPlan_TwelveHooksInOneEventStillSortIntoDeclaredOrder(t *testing.T) {
	var hooks []bundles.BundleHook
	for i := 0; i < 12; i++ {
		hooks = append(hooks, bundles.BundleHook{Type: "command", Command: "cmd"})
	}
	b := &bundles.Bundle{Name: "vault", Hooks: bundles.BundleHooks{PreTool: hooks}}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)

	names := refNames(items, trust.KindHook)
	require.Len(t, names, 12)
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	assert.Equal(t, names, sorted,
		"with 12 hooks the padded ordinals must still sort into declared order")
}
