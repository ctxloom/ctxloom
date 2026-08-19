package backends

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// --- event coverage ---------------------------------------------------------

// TestManagedHooks_EveryUnifiedEventIsCovered turns the drift wire.HooksConfig.Append
// warns about into a failing test. HookEvents/unifiedEventHooks/setUnifiedEventHooks
// enumerate the six events by hand; a SEVENTH field added to wire.UnifiedHooks
// and not added here would not fail to compile — it would silently never be
// assembled, never reported, and never written, which is the same shape as the
// bug the shared resolution point exists to prevent.
func TestManagedHooks_EveryUnifiedEventIsCovered(t *testing.T) {
	typ := reflect.TypeOf(wire.UnifiedHooks{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("yaml")
		fields = append(fields, strings.Split(tag, ",")[0])
	}
	events := HookEvents()
	sortedFields := append([]string(nil), fields...)
	sortedEvents := append([]string(nil), events...)
	sort.Strings(sortedFields)
	sort.Strings(sortedEvents)
	require.Equal(t, sortedEvents, sortedFields,
		"every wire.UnifiedHooks event must be in HookEvents(), or it is assembled by nothing and reported by nobody")

	// And the accessors must actually reach each one, in both directions.
	for _, event := range events {
		var u wire.UnifiedHooks
		marker := []wire.Hook{{Command: "marker-" + event}}
		setUnifiedEventHooks(&u, event, marker)
		assert.Equal(t, marker, unifiedEventHooks(u, event), "accessors must round-trip %q", event)
	}
}

// --- provenance -------------------------------------------------------------

// sourcesByCommand indexes an assembled event by hook command.
func sourcesByCommand(m *ManagedHooks, event string) map[string]HookSource {
	out := make(map[string]HookSource)
	for _, h := range m.For(event) {
		out[h.Hook.Command] = h.Source
	}
	return out
}

// TestAssembleManagedHooks_ProvenanceDistinguishesConfigFromInlineProfile is the
// specific gap the model closes. Both blocks live in config.yaml and neither
// stamps a marker on its hooks, so after a pure-append merge they were the same
// coarse "local" — a user told "local" still had to search two blocks. The merge
// always knew; now it says.
func TestAssembleManagedHooks_ProvenanceDistinguishesConfigFromInlineProfile(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "from-config", Type: "command"}},
		}},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"dev"}}},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"dev": {Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
				PreTool: []wire.Hook{{Command: "from-inline-profile", Type: "command"}},
			}}},
		}},
	})

	got := sourcesByCommand(AssembleManagedHooks(cfg, "/tmp", "", nil), "pre_tool")

	assert.Equal(t, HookSource{Origin: HookOriginConfig}, got["from-config"])
	assert.Equal(t, HookSource{Origin: HookOriginProfileInline, Profile: "dev"}, got["from-inline-profile"],
		"an inline profile's hooks must name the PROFILE, not be flattened in with config-level ones")
}

// TestAssembleManagedHooks_ProvenanceNamesDirectoryProfileAndItsBundles covers
// the remaining source kinds through the production resolution path: a directory
// profile's own hooks, and the hooks of a bundle that profile references — which
// arrive in one flat set alongside the builtins and are told apart by the marker
// the bundle extractor stamped.
func TestAssembleManagedHooks_ProvenanceNamesDirectoryProfileAndItsBundles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "kit.yaml"), []byte(
		"version: \"1.0\"\nhooks:\n  pre_tool:\n    - command: from-bundle\n      type: command\n"), 0o644))
	profilesDir := paths.ProfilesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte(
		"bundles:\n  - kit\nhooks:\n  unified:\n    pre_tool:\n      - command: from-dir-profile\n        type: command\n"), 0o644))
	cfg := config.NewFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"dev"}}},
	})
	cfg.DisableCompanionProbe()

	got := sourcesByCommand(AssembleManagedHooks(cfg, "/tmp", "", nil), "pre_tool")

	assert.Equal(t, HookOriginProfileDirectory, got["from-dir-profile"].Origin)
	assert.Equal(t, "dev", got["from-dir-profile"].Profile)
	assert.Equal(t, HookOriginBundle, got["from-bundle"].Origin,
		"a bundle a profile references must be told apart from the profile's OWN hooks")
	assert.Contains(t, got["from-bundle"].Ref, "kit",
		"the bundle origin must name the bundle, since that is the thing a user goes and changes")
	assert.Empty(t, got["from-bundle"].Profile,
		"which profile pulled a bundle in does not survive the flat bundle pass, and must not be invented")
}

// TestBundleSource_ClassifiesEveryClass pins the read-back of the one piece of
// provenance the flat bundle pass carries. Builtin, companion and
// profile-referenced bundles (local, and the two remote classes, git and
// file) are genuinely different answers to "what do I change", so they must
// not collapse into one — walked over every trust.SourceClass value, not just
// the two internal ones a raw string-prefix test could tell apart.
//
// The marker is config.extractHooksFromBundle's canonical
// "bundle:" + src.BundleIdentity() — bundleSource classifies it by PARSED
// CLASS (trust.ParseBundleRef), never a string prefix, so this pins that
// classification against every class the grammar defines, not the two the old
// prefix test happened to check.
func TestBundleSource_ClassifiesEveryClass(t *testing.T) {
	mustRef := func(br trust.BundleRef, err error) trust.BundleRef {
		require.NoError(t, err)
		return br
	}
	cases := []struct {
		name string
		scm  string
		want HookSource
	}{
		{
			name: "builtin",
			scm:  "bundle:" + mustRef(trust.BuiltinRef("core")).BundleIdentity(),
			want: HookSource{Origin: HookOriginBuiltin, Ref: "ctxloom+builtin:core"},
		},
		{
			name: "companion",
			scm:  "bundle:" + mustRef(trust.CompanionRef("ltk")).BundleIdentity(),
			want: HookSource{Origin: HookOriginCompanion, Ref: "ctxloom+companion:ltk"},
		},
		{
			name: "local",
			scm:  "bundle:" + mustRef(trust.LocalRef("local-kit")).BundleIdentity(),
			want: HookSource{Origin: HookOriginBundle, Ref: "ctxloom+local:local-kit"},
		},
		{
			name: "git",
			scm:  "bundle:" + mustRef(trust.GitRef("github.com", "/acme/tools", "kit")).BundleIdentity(),
			want: HookSource{Origin: HookOriginBundle, Ref: "ctxloom+git://github.com/acme/tools//bundles/kit"},
		},
		{
			name: "file",
			scm:  "bundle:" + mustRef(trust.FileRef("/srv/repo", "kit")).BundleIdentity(),
			want: HookSource{Origin: HookOriginBundle, Ref: "ctxloom+file:///srv/repo//bundles/kit"},
		},
		// A marker that fails to parse (the retired, non-canonical spelling
		// included) is answered "I do not know" rather than the nearest
		// plausible guess — a confident wrong attribution sends someone to
		// edit the wrong file.
		{
			name: "unparseable marker",
			scm:  "bundle:builtin:core",
			want: HookSource{Origin: HookOriginUnattributed, Ref: "builtin:core"},
		},
		// No marker at all: unreachable in practice, and answered the same way.
		{name: "empty SCM", scm: "", want: HookSource{Origin: HookOriginUnattributed}},
		{name: "no bundle: prefix", scm: "something-else", want: HookSource{Origin: HookOriginUnattributed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, bundleSource(wire.Hook{SCM: tc.scm}), "SCM %q", tc.scm)
		})
	}
}

// TestAssembleManagedHooks_ContextInjectionIsAttributedToContext keeps the
// synthesised hook honest: it is authored nowhere, so it must not be reported as
// if some file declared it.
func TestAssembleManagedHooks_ContextInjectionIsAttributedToContext(t *testing.T) {
	m := AssembleManagedHooks(config.NewFixture(config.Fixture{}), t.TempDir(), "deadbeef", nil)

	hooks := m.For("session_start")
	require.NotEmpty(t, hooks, "a non-empty context hash must synthesise the injection hook")
	for _, h := range hooks {
		assert.Equal(t, HookOriginContext, h.Source.Origin)
	}
}

// TestAssembleManagedHooks_DeclaredPositionsAreContiguous: Declared is what
// "before the reorder" means, so it has to be a real 1-based position within the
// event rather than a per-source index that restarts at every merge.
func TestAssembleManagedHooks_DeclaredPositionsAreContiguous(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "c1"}, {Command: "c2"}},
		}},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"dev"}}},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"dev": {Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
				PreTool: []wire.Hook{{Command: "p1"}, {Command: "p2"}},
			}}},
		}},
	})

	hooks := AssembleManagedHooks(cfg, "/tmp", "", nil).For("pre_tool")

	require.Len(t, hooks, 4)
	for i, h := range hooks {
		assert.Equal(t, i+1, h.Declared, "declared positions number the EVENT, not each source")
	}
}

// --- reorder ----------------------------------------------------------------

// eventWithCommands assembles a model whose pre_tool event is exactly these
// commands, in this order.
func eventWithCommands(cmds ...string) *ManagedHooks {
	hooks := make([]wire.Hook, 0, len(cmds))
	for _, c := range cmds {
		hooks = append(hooks, wire.Hook{Command: c, Type: "command"})
	}
	return AssembleManagedHooks(config.NewFixture(config.Fixture{
		Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: hooks}},
	}), "/tmp", "", nil)
}

func commandsOf(hooks []ResolvedHook) []string {
	out := make([]string, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, h.Hook.Command)
	}
	return out
}

// TestManagedHooks_ReorderIsAPermutation is the property that makes the reorder
// surface safe to expose at all. A reorder that could DROP a hook is a silent
// security change — a pre_tool guard that stops running — dressed as a
// formatting preference. Adversarial rankers, including contradictory and
// degenerate ones, must all produce the same multiset.
func TestManagedHooks_ReorderIsAPermutation(t *testing.T) {
	// More than nine, because that is where a width or lexical bug shows.
	var cmds []string
	for i := 0; i < 12; i++ {
		cmds = append(cmds, "cmd-"+string(rune('a'+i)))
	}
	rankers := map[string]HookRanker{
		"everything ranked identically": func(ResolvedHook) (int, bool) { return 7, true },
		"nothing ranked":                func(ResolvedHook) (int, bool) { return 0, false },
		"reverse of declared":           func(h ResolvedHook) (int, bool) { return -h.Declared, true },
		"alternating unranked":          func(h ResolvedHook) (int, bool) { return h.Declared, h.Declared%2 == 0 },
		"negative and huge ranks":       func(h ResolvedHook) (int, bool) { return (h.Declared%3)*1000000 - 500000, true },
	}
	for name, rank := range rankers {
		t.Run(name, func(t *testing.T) {
			m := eventWithCommands(cmds...)
			require.NoError(t, m.Reorder("pre_tool", rank))

			got := commandsOf(m.For("pre_tool"))
			require.Len(t, got, len(cmds), "a reorder must not change how many hooks fire")
			sorted := append([]string(nil), got...)
			want := append([]string(nil), cmds...)
			sort.Strings(sorted)
			sort.Strings(want)
			assert.Equal(t, want, sorted, "a reorder must be a permutation: same hooks, new order")
		})
	}
}

// TestManagedHooks_ReorderMovesRankedHooksAheadAndKeepsBlocks is the shape a
// project-level ordering rule needs: naming a source moves ITS hooks as a
// contiguous block, preserving their order among themselves, because otherwise
// the override would silently destroy the ordering a bundle declared for itself.
func TestManagedHooks_ReorderMovesRankedHooksAheadAndKeepsBlocks(t *testing.T) {
	m := eventWithCommands("a1", "b1", "a2", "c1", "b2")
	block := map[string]int{"b1": 0, "b2": 0, "a1": 1, "a2": 1}

	require.NoError(t, m.Reorder("pre_tool", func(h ResolvedHook) (int, bool) {
		r, ok := block[h.Hook.Command]
		return r, ok
	}))

	assert.Equal(t, []string{"b1", "b2", "a1", "a2", "c1"}, commandsOf(m.For("pre_tool")),
		"ranked hooks lead in rank order, each rank's members keep their relative order, and unranked hooks follow unchanged")
}

// TestManagedHooks_ReorderKeepsUnrankedHooksLast: a hook nobody mentioned must
// not overtake one someone did — the same absent-sorts-last reasoning as
// wire.HookOrderLess.
func TestManagedHooks_ReorderKeepsUnrankedHooksLast(t *testing.T) {
	m := eventWithCommands("first", "second", "third")

	require.NoError(t, m.Reorder("pre_tool", func(h ResolvedHook) (int, bool) {
		return 0, h.Hook.Command == "third"
	}))

	assert.Equal(t, []string{"third", "first", "second"}, commandsOf(m.For("pre_tool")))
}

// TestManagedHooks_ReorderPreservesDeclaredPositions: Declared is the record of
// what the merge produced. A reorder that rewrote it would erase the only
// evidence that anything moved, leaving a user asking "why is this third" with
// the same non-answer they started with.
func TestManagedHooks_ReorderPreservesDeclaredPositions(t *testing.T) {
	m := eventWithCommands("one", "two", "three")

	require.NoError(t, m.Reorder("pre_tool", func(h ResolvedHook) (int, bool) {
		return 0, h.Hook.Command == "three"
	}))

	hooks := m.For("pre_tool")
	require.Len(t, hooks, 3)
	assert.Equal(t, "three", hooks[0].Hook.Command)
	assert.Equal(t, 3, hooks[0].Declared, "the moved hook still reports where the merge put it")
	assert.Equal(t, 1, hooks[1].Declared)
	assert.Equal(t, 2, hooks[2].Declared)
}

// TestManagedHooks_ReorderIsVisibleToInspectionAndWireAlike is the constraint
// the whole seam exists for, made executable: with a reorder in force, the order
// inspection reports and the order a settings writer serializes are the same
// sequence. If these could differ, the object would be worse than no object.
func TestManagedHooks_ReorderIsVisibleToInspectionAndWireAlike(t *testing.T) {
	m := eventWithCommands("alpha", "beta", "gamma")

	require.NoError(t, m.Reorder("pre_tool", func(h ResolvedHook) (int, bool) {
		return map[string]int{"gamma": 0, "beta": 1, "alpha": 2}[h.Hook.Command], true
	}))

	inspected := commandsOf(m.For("pre_tool"))
	var written []string
	for _, h := range m.Wire().Unified.PreTool {
		written = append(written, h.Command)
	}
	assert.Equal(t, []string{"gamma", "beta", "alpha"}, inspected)
	assert.Equal(t, inspected, written,
		"what `manage hooks list` shows and what lands in an engine's settings file must be the same order")
}

// TestManagedHooks_ReorderRefusesWhatItCannotDo: an unknown event that quietly
// reordered nothing is indistinguishable from a rule that had no effect, which
// is ctxloom's characteristic failure — exit 0 and nothing happened.
func TestManagedHooks_ReorderRefusesWhatItCannotDo(t *testing.T) {
	m := eventWithCommands("a", "b")

	err := m.Reorder("pre_toll", func(ResolvedHook) (int, bool) { return 0, true })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre_toll")
	assert.Contains(t, err.Error(), "pre_tool", "the error must name the events that DO exist")

	require.Error(t, m.Reorder("pre_tool", nil), "a nil ranker is a caller error, not a no-op")

	var nilModel *ManagedHooks
	require.Error(t, nilModel.Reorder("pre_tool", func(ResolvedHook) (int, bool) { return 0, true }))

	assert.Equal(t, []string{"a", "b"}, commandsOf(m.For("pre_tool")),
		"a refused reorder must leave the order untouched")
}

// TestManagedHooks_ReorderOfAnEmptyEventIsNotAnError: "no hooks fire on this
// event" is a real state, not a mistake — unlike a misspelled event name.
func TestManagedHooks_ReorderOfAnEmptyEventIsNotAnError(t *testing.T) {
	m := eventWithCommands("a")
	assert.NoError(t, m.Reorder("session_end", func(ResolvedHook) (int, bool) { return 0, true }))
	assert.Empty(t, m.For("session_end"))
}

// --- inspection accessors ---------------------------------------------------

// TestManagedHooks_BackendNativeIsSortedAndOmitsTheEmptyKeys: the empty backend
// and event keys the append merge creates are structure Wire has to reproduce,
// not hooks anyone asked about — reporting them would invent rows for hooks that
// do not exist.
func TestManagedHooks_BackendNativeIsSortedAndOmitsTheEmptyKeys(t *testing.T) {
	m := AssembleManagedHooks(config.NewFixture(config.Fixture{Hooks: wire.HooksConfig{
		Plugins: map[string]wire.BackendHooks{
			"zed":    {"PreCompact": []wire.Hook{{Command: "z-native"}}},
			"claude": {"PreToolUse": []wire.Hook{{Command: "c-native"}}, "PostToolUse": {}},
			"codex":  {},
		},
	}}), "/tmp", "", nil)

	native := m.BackendNative()

	require.Len(t, native, 2, "only backend events with hooks are reported")
	assert.Equal(t, "claude", native[0].Backend)
	assert.Equal(t, "PreToolUse", native[0].Event)
	assert.Equal(t, HookOriginConfig, native[0].Hooks[0].Source.Origin)
	assert.Equal(t, "zed", native[1].Backend, "sorted by backend, so the report can be diffed between runs")

	// ...and the empty keys still reach the wire projection.
	require.Contains(t, m.Wire().Plugins, "codex")
	require.Contains(t, m.Wire().Plugins["claude"], "PostToolUse")
}

// TestHookSource_StringNamesWhateverTheOriginCarries keeps the human label
// honest for each shape of source.
func TestHookSource_StringNamesWhateverTheOriginCarries(t *testing.T) {
	assert.Equal(t, "config", HookSource{Origin: HookOriginConfig}.String())
	assert.Equal(t, "profile-inline dev", HookSource{Origin: HookOriginProfileInline, Profile: "dev"}.String())
	assert.Equal(t, "bundle acme/tools", HookSource{Origin: HookOriginBundle, Ref: "acme/tools"}.String())
}

// TestManagedHooks_NilModelIsInertRatherThanPanicking: AssembleManagedHooks is
// fault-tolerant by contract (a config load failure yields an empty managed set
// rather than blocking a launch), so every accessor has to survive the degraded
// case.
func TestManagedHooks_NilModelIsInert(t *testing.T) {
	var m *ManagedHooks
	assert.Nil(t, m.For("pre_tool"))
	assert.Nil(t, m.BackendNative())
	assert.Equal(t, &wire.HooksConfig{Plugins: map[string]wire.BackendHooks{}}, m.Wire())
}
