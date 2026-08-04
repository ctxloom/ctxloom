package convert

import (
	"context"
	"os"
	"regexp"
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

// intp makes an order literal addressable. Hook.Order is a POINTER so "declared
// 0" and "declared nothing" stay distinguishable.
func intp(v int) *int { return &v }

// plannedHooks returns the planned hook surfaces in PLAN order.
func plannedHooks(items []Item) []content.Hook {
	var out []content.Hook
	for _, it := range items {
		if h, ok := it.Surface.(content.Hook); ok {
			out = append(out, h)
		}
	}
	return out
}

// convertToFiles converts a bundle into a fresh tree and returns every file it
// wrote, keyed by path. Comparing two of these is how a test asserts that a
// change to one item left the others' BYTES alone — the property positional
// identity destroys and this whole change exists to restore.
func convertToFiles(t *testing.T, ctx context.Context, b *bundles.Bundle) map[string]string {
	t.Helper()
	st, fsys := newStore(t)
	require.NoError(t, Convert(ctx, st, "vault", b, Options{}))

	out := map[string]string{}
	require.NoError(t, afero.Walk(fsys, "/tree", func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, rerr := afero.ReadFile(fsys, p)
		if rerr != nil {
			return rerr
		}
		out[p] = string(data)
		return nil
	}))
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
// and hooks merge by pure append, so sequence is meaning. In the tree, a hook's
// FILENAME is identity only — so the converter has to move that sequence into the
// order field, and a converter that got it wrong would produce a tree that is
// byte-identical to a correct one while executing hooks in the wrong order.
func TestPlan_HookOrderCarriesDeclaredSequenceWhileNamesStayAlphabetical(t *testing.T) {
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

	hooks := plannedHooks(items)
	require.Len(t, hooks, 2)

	// Order is DATA now, assigned sparsely in declared sequence.
	assert.Equal(t, "echo stamp", hooks[0].Command)
	require.NotNil(t, hooks[0].Order)
	assert.Equal(t, content.HookOrderStep, *hooks[0].Order)
	assert.Equal(t, "echo audit", hooks[1].Command)
	require.NotNil(t, hooks[1].Order)
	assert.Equal(t, 2*content.HookOrderStep, *hooks[1].Order)

	// And the name is NOT a carrier: alphabetical order is the reverse of
	// declared order here, which is exactly the disagreement the field absorbs.
	names := refNames(items, trust.KindHook)
	assert.Equal(t, []string{"post_file_edit/echo-stamp", "post_file_edit/echo-audit"}, names)

	content.SortHooks(hooks)
	assert.Equal(t, "echo stamp", hooks[0].Command,
		"sorting by the order field must reproduce the DECLARED order, not the name order")
}

// The `<NN>-` ordinal prefix is REMOVED, not carried alongside. Leaving it would
// mean two carriers of the same fact, which drift, and would keep the positional
// identity the field exists to delete: an insert renames every hook below it.
func TestPlan_HookNamesCarryNoOrdinalPrefix(t *testing.T) {
	b := &bundles.Bundle{
		Name: "vault",
		Hooks: bundles.BundleHooks{
			PreTool: []bundles.BundleHook{
				{Type: "command", Command: "guard"},
				{Type: "command", Command: "audit"},
			},
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)

	ordinal := regexp.MustCompile(`^\d+-`)
	for _, name := range refNames(items, trust.KindHook) {
		_, base, _ := strings.Cut(name, "/")
		assert.False(t, ordinal.MatchString(base),
			"hook name %q still leads with an ordinal; the filename must carry identity only", name)
	}
	assert.Equal(t, []string{"pre_tool/guard", "pre_tool/audit"}, refNames(items, trust.KindHook))
}

// A hook that DECLARES its own order keeps it. The converter assigns order only
// where the author did not — overwriting an authored value would silently discard
// the one thing in the source document that says what the author meant.
func TestPlan_AuthoredHookOrderIsPreservedNotOverwritten(t *testing.T) {
	b := &bundles.Bundle{
		Name: "vault",
		Hooks: bundles.BundleHooks{
			PreTool: []bundles.BundleHook{
				{Type: "command", Command: "guard", Order: intp(4242)},
				{Type: "command", Command: "audit"},
			},
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)

	hooks := plannedHooks(items)
	require.Len(t, hooks, 2)
	require.NotNil(t, hooks[0].Order)
	assert.Equal(t, 4242, *hooks[0].Order, "an authored order must survive conversion verbatim")
	require.NotNil(t, hooks[1].Order)
	assert.Equal(t, 2*content.HookOrderStep, *hooks[1].Order,
		"a hook with no authored order still gets its declared position, by position")
}

// Names are synthesised from CONTENT, so two hooks with the same command collide.
// Under the retired ordinal the prefix hid that; without it, a collision would
// make one hook overwrite the other — a hook that converts, signs and
// materializes perfectly happily while simply not being there.
func TestPlan_HooksWithIdenticalContentGetDistinctNames(t *testing.T) {
	b := &bundles.Bundle{
		Name: "vault",
		Hooks: bundles.BundleHooks{
			PreTool: []bundles.BundleHook{
				{Type: "command", Command: "same"},
				{Type: "command", Command: "same"},
				{Type: "command", Command: "same"},
			},
		},
	}
	items, err := Plan("vault", b, Options{})
	require.NoError(t, err)

	names := refNames(items, trust.KindHook)
	require.Len(t, names, 3)
	seen := map[string]bool{}
	for _, n := range names {
		assert.False(t, seen[n], "hook name %q was emitted twice; one hook would overwrite the other", n)
		seen[n] = true
	}
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

	// ORDER, read back through the real enumeration. The walk yields these two
	// name-sorted ("echo-audit" before "echo-stamp"), which is NOT declared order
	// — resolving them means reading the field.
	var readBack []content.Hook
	for _, name := range got[trust.KindHook] {
		it, err := bun.Item(ctx, trust.Ref{Bundle: "vault", Kind: trust.KindHook, Name: name})
		require.NoError(t, err)
		surf, err := it.Surface(ctx)
		require.NoError(t, err)
		h, ok := surf.(content.Hook)
		require.True(t, ok)
		readBack = append(readBack, h)
	}
	content.SortHooks(readBack)
	assert.Equal(t, "echo stamp", readBack[0].Command,
		"resolving the tree's hooks must yield the FIRST-DECLARED hook first")
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
		// The envelope is the one non-item file a conversion writes: it carries
		// the bundle-level metadata no item can supply, and bundles.ReadTree
		// refuses a tree without it.
		if f == bundles.DirectoryFormManifest {
			continue
		}
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

// Twelve hooks in one event, converted OUT and read BACK IN through the real
// enumeration. Ten is the threshold at which any width-or-lexical-sort bug shows:
// "1000" precedes "200" under string comparison, and the names here are chosen so
// alphabetical order is the exact REVERSE of declared order.
//
// This is the assertion that a tree holding the right bytes in the wrong order
// cannot pass, and it is worth stating why it needs both directions: the encode
// side alone would pass against a converter that assigned order correctly and a
// decoder that dropped it on the floor.
func TestConvert_TwelveHooksRoundTripBackIntoTheirDeclaredOrder(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()

	// Commands "cmd-l" … "cmd-a": declared order is the reverse of alphabetical,
	// and the names the converter synthesises are derived from those commands.
	var declared []string
	var hooks []bundles.BundleHook
	for i := 0; i < 12; i++ {
		cmd := "cmd-" + string(rune('l'-i))
		declared = append(declared, cmd)
		hooks = append(hooks, bundles.BundleHook{Type: "command", Command: cmd})
	}
	b := &bundles.Bundle{Name: "vault", Hooks: bundles.BundleHooks{PreTool: hooks}}
	require.NoError(t, Convert(ctx, st, "vault", b, Options{}))

	bun, err := st.Open(ctx, "vault")
	require.NoError(t, err)
	refs, err := bun.Refs(ctx)
	require.NoError(t, err)

	var got []content.Hook
	for _, r := range refs {
		if r.Kind != trust.KindHook {
			continue
		}
		item, err := bun.Item(ctx, r)
		require.NoError(t, err)
		surf, err := item.Surface(ctx)
		require.NoError(t, err)
		h, ok := surf.(content.Hook)
		require.True(t, ok)
		require.NotNil(t, h.Order, "hook %q came back with NO order; the sidecar was written but not read", r.Name)
		got = append(got, h)
	}
	require.Len(t, got, 12)

	// The walk yields them sorted by NAME, which here is the reverse of declared
	// order — so the test is discriminating only because it re-sorts by the field.
	var walkOrder []string
	for _, h := range got {
		walkOrder = append(walkOrder, h.Command)
	}
	require.NotEqual(t, declared, walkOrder,
		"fixture is not discriminating: the walk already yields declared order")

	content.SortHooks(got)
	var resolved []string
	for _, h := range got {
		resolved = append(resolved, h.Command)
	}
	assert.Equal(t, declared, resolved,
		"twelve hooks read back out of the tree must resolve into their DECLARED order")
}

// The insert property, stated end to end: adding a hook to an event must leave
// every OTHER hook's files byte-identical. Under `<NN>-<slug>` an insert renamed
// the tail, staling countersignatures for hooks that had not changed.
//
// The hooks carry AUTHORED orders, which is the point being made: once order is
// data, sequence stops being a function of position, so a hook declared FIRST can
// run in the middle and nothing else moves. A converter-assigned order is
// positional of necessity — the source document carries nothing else — so it is
// the authored case that has to hold.
func TestConvert_InsertingAHookLeavesEveryOtherHooksFilesByteIdentical(t *testing.T) {
	ctx := context.Background()
	base := []bundles.BundleHook{
		{Type: "command", Command: "guard", Order: intp(100)},
		{Type: "command", Command: "audit", Order: intp(200)},
	}
	inserted := append([]bundles.BundleHook{
		// Declared FIRST in the document, but ordered into the gap between the
		// other two — so it runs second and renames nothing.
		{Type: "command", Command: "stamp", Order: intp(150)},
	}, base...)

	before := convertToFiles(t, ctx, &bundles.Bundle{Name: "vault", Hooks: bundles.BundleHooks{PreTool: base}})
	after := convertToFiles(t, ctx, &bundles.Bundle{Name: "vault", Hooks: bundles.BundleHooks{PreTool: inserted}})

	for path, want := range before {
		got, ok := after[path]
		require.True(t, ok, "inserting a hook DELETED %q", path)
		assert.Equal(t, want, got, "inserting a hook rewrote %q — positional identity is back", path)
	}
	assert.Greater(t, len(after), len(before), "the inserted hook wrote no files at all")
}
