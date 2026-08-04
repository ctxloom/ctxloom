package backends

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file is the SAFETY NET for turning AssembleManagedHooks' return value
// into a resolved model whose Wire() is a projection.
//
// assembleManagedHooksReference is a FROZEN copy of the assembly as it stood
// before the model existed: the same merge sequence (config → per-profile
// inline-or-gated-directory → bundle-shipped → context injection) expressed
// with agent.MergeHooksConfig, the pure-append merge the writers have always
// been fed. TestAssembleManagedHooks_WireMatchesFrozenReference asserts the
// model's Wire() is DEEPLY equal to it across every shape the assembly can
// produce, so the writers — which serialize this value into .claude/settings.json,
// codex config.toml, and every other engine settings surface — keep behaving
// exactly as they did.
//
// It is deliberately NOT a golden file: encoding/json's omitempty erases the
// difference between a nil slice, an empty slice, and an absent map key, and
// the plugin half of the merge creates map keys whose values are nil slices.
// A golden would call that regression green. reflect.DeepEqual does not.
//
// What this pins is the ASSEMBLY (which sources merge, in what order, with what
// nil-vs-empty structure). It shares the gate/scope/resolve helpers with the
// production path on purpose: those are not what this refactor moves, and a
// twin that re-implemented them would pin its own bugs.
func assembleManagedHooksReference(cfg *config.Config, workDir, contextHash string, profileNames []string) *wire.HooksConfig {
	hooks := &wire.HooksConfig{Plugins: make(map[string]wire.BackendHooks)}
	if cfg == nil {
		return hooks
	}
	configHooks := cfg.GetHooksConfig()
	agent.MergeHooksConfig(hooks, &configHooks)
	gate := cfg.ExecutableTrustGate()
	profileNamesScoped := scopedProfiles(cfg, profileNames)
	profileDefs := cfg.GetProfileDefinitions()
	for _, profileName := range profileNamesScoped {
		inlineResolved, inlineErr := config.ResolveProfile(profileDefs, profileName)
		if inlineErr == nil {
			agent.MergeHooksConfig(hooks, &inlineResolved.Hooks)
			continue
		}
		if !errors.Is(inlineErr, errs.ErrProfileNotFound) {
			continue
		}
		resolved, err := cfg.GetProfileLoader().ResolveProfile(profileName, nil)
		if err != nil {
			continue
		}
		gated := gateProfileHooks(profileGateRefFor(resolved, profileName), resolved.Hooks, gate)
		agent.MergeHooksConfig(hooks, &gated)
	}
	hooks.Unified.Append(cfg.ResolveBundleHooks(profileNamesScoped))
	if contextHash != "" {
		hooks.Unified.SessionStart = append(hooks.Unified.SessionStart,
			agent.NewContextInjectionHooks(contextHash, workDir)...)
	}
	return hooks
}

// hookParityCase is one assembly shape to compare.
//
// present/absent are the ANTI-VACUITY guard. Equality between two functions
// that both resolved to nothing is trivially true, and most of these fixtures
// reach the assembly through a filesystem the test builds — one wrong path and
// a case silently stops proving anything while still passing. Naming the
// marker commands each case must (and must not) produce makes that failure
// loud.
type hookParityCase struct {
	name        string
	cfg         func(t *testing.T) *config.Config
	workDir     string
	contextHash string
	profiles    []string
	present     []string
	absent      []string
}

// allHookCommands returns every command in an assembled set — unified events
// and backend-native alike — for the present/absent guard.
func allHookCommands(h *wire.HooksConfig) []string {
	var out []string
	for _, hooks := range [][]wire.Hook{
		h.Unified.PreTool, h.Unified.PostTool, h.Unified.SessionStart,
		h.Unified.SessionEnd, h.Unified.PreShell, h.Unified.PostFileEdit,
	} {
		for _, hook := range hooks {
			out = append(out, hook.Command)
		}
	}
	for _, backend := range h.Plugins {
		for _, hooks := range backend {
			for _, hook := range hooks {
				out = append(out, hook.Command)
			}
		}
	}
	return out
}

// localBundleProfileCfg writes a local bundle carrying top-level hooks: and a
// directory profile that REFERENCES it, so the bundle-shipped half of the
// assembly (cfg.ResolveBundleHooks → extractHooksFromBundle, which stamps
// SCM="bundle:<ref>") is exercised rather than only the profile-inline half.
func localBundleProfileCfg(t *testing.T, bundleHooks string) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hooked.yaml"),
		[]byte("name: hooked\nversion: \"1.0\"\n"+bundleHooks), 0o644))
	profilesDir := paths.ProfilesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "withbundle.yaml"),
		[]byte("bundles:\n  - hooked\n"), 0o644))
	cfg := config.NewFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"withbundle"}}},
	})
	cfg.DisableCompanionProbe()
	return cfg
}

// bundleShippedProfileCfg is the gate-ref fixture's shape: a local bundle that
// ships a PROFILE carrying inline hooks, reached through the directory-profile
// fallback branch.
func bundleShippedProfileCfg(t *testing.T, gate bundles.ContentGate) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "kit.yaml"), []byte(""+
		"name: kit\nversion: \"1.0\"\n"+
		"profiles:\n  dev:\n    hooks:\n      unified:\n        pre_tool:\n"+
		"          - command: bundle-shipped-hook\n            type: command\n"+
		"          - command: bundle-shipped-second\n            type: command\n"),
		0o644))
	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents: map[string]agents.Agent{"default": {
			Profiles: []string{remote.LocalBundleRef("kit") + remote.ProfileSelector + "dev"},
		}},
		AppPaths: []string{appDir},
	})
	cfg.DisableCompanionProbe()
	if gate != nil {
		cfg.SetExecutableTrustGate(gate)
	}
	return cfg
}

// hookParityCases enumerates every source combination the assembly merges, plus
// the structural edges the merge produces but a naive rebuild would not: a
// plugin backend key whose event list is EMPTY (the merge creates the key with a
// nil slice), and a backend key with no events at all.
func hookParityCases() []hookParityCase {
	return []hookParityCase{
		{
			name: "nil config",
			cfg:  func(*testing.T) *config.Config { return nil },
		},
		{
			name: "bare fixture, no sources",
			cfg:  func(*testing.T) *config.Config { return config.NewFixture(config.Fixture{}) },
		},
		{
			name: "config-level unified hooks only",
			cfg: func(*testing.T) *config.Config {
				return config.NewFixture(config.Fixture{Hooks: wire.HooksConfig{
					Unified: wire.UnifiedHooks{
						PreTool:      []wire.Hook{{Command: "cfg-pre", Type: "command"}},
						PostTool:     []wire.Hook{{Command: "cfg-post", Type: "command"}},
						SessionStart: []wire.Hook{{Command: "cfg-start", Type: "command"}},
						SessionEnd:   []wire.Hook{{Command: "cfg-end", Type: "command"}},
						PreShell:     []wire.Hook{{Command: "cfg-shell", Type: "command"}},
						PostFileEdit: []wire.Hook{{Command: "cfg-edit", Type: "command"}},
					},
				}})
			},
			present: []string{"cfg-pre", "cfg-post", "cfg-start", "cfg-end", "cfg-shell", "cfg-edit"},
		},
		{
			name: "config-level backend-native hooks, including empty skeletons",

			cfg: func(*testing.T) *config.Config {
				return config.NewFixture(config.Fixture{Hooks: wire.HooksConfig{
					Plugins: map[string]wire.BackendHooks{
						"claude": {
							"PreToolUse": []wire.Hook{{Command: "native-pre", Type: "command"}},
							// An event key whose list is EMPTY: the merge
							// creates the key with a nil value.
							"PostToolUse": {},
						},
						// A backend key with NO events at all: the merge
						// creates the backend key with an empty map.
						"codex": {},
					},
				}})
			},
			present: []string{"native-pre"},
		},
		{
			name: "inline profile hooks merged after config-level",
			cfg: func(*testing.T) *config.Config {
				return config.NewFixture(config.Fixture{
					Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
						PreTool: []wire.Hook{{Command: "cfg-pre", Type: "command"}},
					}},
					DefaultAgent: "default",
					Agents:       map[string]agents.Agent{"default": {Profiles: []string{"p", "q"}}},
					Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
						"p": {Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
							PreTool:      []wire.Hook{{Command: "p-pre", Type: "command"}},
							SessionStart: []wire.Hook{{Command: "p-start", Type: "command"}},
						}}},
						"q": {Hooks: wire.HooksConfig{
							Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "q-pre", Type: "command"}}},
							Plugins: map[string]wire.BackendHooks{"claude": {"PreToolUse": []wire.Hook{{Command: "q-native"}}}},
						}},
					}},
				})
			},
			present: []string{"cfg-pre", "p-pre", "p-start", "q-pre", "q-native"},
		},
		{
			name: "inline profile hooks with an explicit profile selection",
			cfg: func(*testing.T) *config.Config {
				return config.NewFixture(config.Fixture{
					DefaultAgent: "default",
					Agents:       map[string]agents.Agent{"default": {Profiles: []string{"p"}}},
					Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
						"p": {Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
							PreTool: []wire.Hook{{Command: "p-pre", Type: "command"}},
						}}},
						"other": {Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
							PreTool: []wire.Hook{{Command: "other-pre", Type: "command"}},
						}}},
					}},
				})
			},
			profiles: []string{"other"},
			present:  []string{"other-pre"},
			absent:   []string{"p-pre"},
		},
		{
			name: "directory profile hooks, ungated",
			cfg: func(t *testing.T) *config.Config {
				return dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirHookBody}, nil)
			},
			present: []string{"keep-hook", "drop-hook"},
		},
		{
			name: "directory profile hooks, gate withholds one",
			cfg: func(t *testing.T) *config.Config {
				cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirHookBody}, nil)
				keepHash := bundles.HashPayload(hookExecPayload(wire.Hook{Command: "keep-hook", Type: "command"}))
				cfg.SetExecutableTrustGate(func(_ string, payload []byte, _, _ string) bool {
					return bundles.HashPayload(payload) == keepHash
				})
				return cfg
			},
			present: []string{"keep-hook"},
			absent:  []string{"drop-hook"},
		},
		{
			name: "directory profile hooks, gate withholds all",
			cfg: func(t *testing.T) *config.Config {
				cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirHookBody}, nil)
				cfg.SetExecutableTrustGate(testFilter(false))
				return cfg
			},
			absent: []string{"keep-hook", "drop-hook"},
		},
		{
			name:    "bundle-shipped profile hooks through the directory fallback",
			cfg:     func(t *testing.T) *config.Config { return bundleShippedProfileCfg(t, nil) },
			present: []string{"bundle-shipped-hook", "bundle-shipped-second"},
		},
		{
			name: "bundle-shipped profile hooks, gate denies",
			cfg: func(t *testing.T) *config.Config {
				return bundleShippedProfileCfg(t, testFilter(false))
			},
			absent: []string{"bundle-shipped-hook", "bundle-shipped-second"},
		},
		{
			name: "profile-referenced bundle hooks",
			cfg: func(t *testing.T) *config.Config {
				return localBundleProfileCfg(t, "hooks:\n  pre_tool:\n"+
					"    - command: bundled-second\n      type: command\n      order: 200\n"+
					"    - command: bundled-first\n      type: command\n      order: 100\n"+
					"  session_start:\n    - command: bundled-start\n      type: command\n")
			},
			present: []string{"bundled-first", "bundled-second", "bundled-start"},
		},
		{
			name: "profile-referenced bundle hooks with a context hash",
			cfg: func(t *testing.T) *config.Config {
				return localBundleProfileCfg(t, "hooks:\n  session_start:\n    - command: bundled-start\n      type: command\n")
			},
			workDir:     "/tmp/work",
			contextHash: "deadbeefcafe",
			present:     []string{"bundled-start"},
		},
		{
			name: "context hash with no other source",
			cfg: func(*testing.T) *config.Config {
				return config.NewFixture(config.Fixture{})
			},
			workDir:     "/tmp/work",
			contextHash: "deadbeefcafe",
		},
		{
			name: "circular inline profile is skipped",
			cfg: func(*testing.T) *config.Config {
				return config.NewFixture(config.Fixture{
					DefaultAgent: "default",
					Agents:       map[string]agents.Agent{"default": {Profiles: []string{"loopy"}}},
					Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
						"loopy": {Parents: []string{"loopy"}},
					}},
				})
			},
		},
		{
			name: "unresolvable profile name is skipped",
			cfg: func(t *testing.T) *config.Config {
				return dirProfileCfg(t, []string{"nope"}, nil, nil)
			},
		},
	}
}

// TestAssembleManagedHooks_WireMatchesFrozenReference is the writers' safety
// property: for every shape the assembly can produce, what a settings writer
// receives is byte-for-byte what it received before the resolved model existed.
func TestAssembleManagedHooks_WireMatchesFrozenReference(t *testing.T) {
	for _, tc := range hookParityCases() {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := clidiag.SetSink(&buf)
			defer restore()

			want := assembleManagedHooksReference(tc.cfg(t), tc.workDir, tc.contextHash, tc.profiles)
			got := AssembleManagedHooks(tc.cfg(t), tc.workDir, tc.contextHash, tc.profiles).Wire()

			assert.Equal(t, want, got,
				"Wire() must project the resolved model onto exactly the wire config the writers were fed before the model existed")

			commands := allHookCommands(got)
			for _, wanted := range tc.present {
				assert.Contains(t, commands, wanted,
					"this case's fixture stopped reaching the assembly — the equality above is now vacuous")
			}
			for _, unwanted := range tc.absent {
				assert.NotContains(t, commands, unwanted)
			}
		})
	}
}

// TestAssembleManagedHooks_WireDistinguishesNilFromEmpty guards the one
// difference reflect.DeepEqual sees and every serializer hides: the merge
// creates a plugin backend key whose event list is a NIL slice, and a backend
// key whose map is EMPTY. A rebuild that dropped either would still marshal
// identically, and would still be a change to what the model claims is there.
func TestAssembleManagedHooks_WireDistinguishesNilFromEmpty(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{Hooks: wire.HooksConfig{
		Plugins: map[string]wire.BackendHooks{
			"claude": {"PostToolUse": {}},
			"codex":  {},
		},
	}})

	got := AssembleManagedHooks(cfg, "", "", nil).Wire()

	require.Contains(t, got.Plugins, "claude")
	require.Contains(t, got.Plugins["claude"], "PostToolUse", "an empty event list must survive as a PRESENT key")
	assert.Nil(t, got.Plugins["claude"]["PostToolUse"], "the merge stores an empty event list as a nil slice")
	require.Contains(t, got.Plugins, "codex", "a backend key with no events must survive")
	assert.Empty(t, got.Plugins["codex"])
	assert.Nil(t, got.Unified.PreTool, "an event with no hooks stays nil, never an empty slice")
}
