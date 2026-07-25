package confload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testProduct is a generic (non-ctxloom) Product used to exercise confload's
// algorithm in isolation, proving it carries no ctxloom-specific assumption.
// knownPaths is a tiny fake "schema" — the set of dotted paths (lower-cased,
// "_"-joined segments) the fake product recognizes as legitimate-but-possibly-
// unset, standing in for a real product's JSON schema.
func testProduct(knownPaths ...string) Product {
	known := map[string]bool{}
	for _, p := range knownPaths {
		known[p] = true
	}
	return Product{
		Name:      "testprod",
		DirName:   ".testprod",
		FileName:  "config.yaml",
		EnvPrefix: "TESTPROD_CONFIG_",
		KnownPath: func(path []string) bool {
			return known[strings.Join(path, ".")]
		},
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// configSetFlagSet builds a *pflag.FlagSet carrying only the --config-set flag
// (confload.ConfigSetFlagName), pre-populated with entries -- the shape every real
// caller (internal/cli/root.go's PersistentPreRun) hands ReadOverrides.
func configSetFlagSet(t *testing.T, entries ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.StringArray(ConfigSetFlagName, nil, "")
	for _, e := range entries {
		require.NoError(t, fs.Set(ConfigSetFlagName, e))
	}
	return fs
}

// TestPrecedence_FullChain_HomeProjectEnvCLI proves the full four-layer chain
// -- home < project < env < cli -- end to end through confload alone (no
// ctxloom-specific code involved), one key overridden at each layer plus one
// left untouched to prove lower layers still show through.
func TestPrecedence_FullChain_HomeProjectEnvCLI(t *testing.T) {
	dir := t.TempDir()
	homePath := filepath.Join(dir, "home.yaml")
	projectPath := filepath.Join(dir, "project.yaml")

	writeFile(t, homePath, "a: home\nb: home\nc: home\nd: home\n")
	writeFile(t, projectPath, "b: project\nc: project\nd: project\n")

	t.Setenv("TESTPROD_CONFIG_C", "env")
	t.Setenv("TESTPROD_CONFIG_D", "env")

	fs := configSetFlagSet(t, "d=cli")

	p := testProduct()
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)

	result, err := p.Load(Sources{HomePath: homePath, ProjectPath: projectPath}, o)
	require.NoError(t, err)

	assert.Equal(t, "home", result["a"], "layer with no override at any higher level stays home's")
	assert.Equal(t, "project", result["b"], "project beats home")
	assert.Equal(t, "env", result["c"], "env beats project")
	assert.Equal(t, "cli", result["d"], "cli (--config-set) beats env")
}

// TestEnvOverlay_ResolvesCaseInsensitivelyToExistingKey proves an env var
// whose name necessarily lost the original key's casing still resolves to
// it, ADOPTING that casing (case 1 of resolvePath).
func TestEnvOverlay_ResolvesCaseInsensitivelyToExistingKey(t *testing.T) {
	t.Setenv("TESTPROD_CONFIG_AGENTS_MYCODER_RUNTIME", "container")

	p := testProduct()
	base := map[string]any{
		"agents": map[string]any{
			// Both the agent label AND the field it targets are spelled with
			// non-lowercase casing an env var's name cannot itself carry --
			// resolution must still find them case-insensitively and ADOPT
			// this exact casing (case 1), not lower-case either segment.
			"MyCoder": map[string]any{
				"engine":  "claude-code",
				"Runtime": "host",
			},
		},
	}
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)

	agents, ok := out["agents"].(map[string]any)
	require.True(t, ok)
	mycoder, ok := agents["MyCoder"].(map[string]any)
	require.True(t, ok, "must adopt the existing key's casing, not lower-case it")
	assert.Equal(t, "container", mycoder["Runtime"], "must adopt the existing field's casing too")
	assert.Equal(t, "claude-code", mycoder["engine"], "sibling key from the file layer must survive")
}

// TestEnvOverlay_AmbiguousCaseIsError proves that when an env var's name
// could refer to more than one existing key (case-fold collision), resolution
// errors out naming the variable and both candidates, rather than guessing.
func TestEnvOverlay_AmbiguousCaseIsError(t *testing.T) {
	t.Setenv("TESTPROD_CONFIG_AGENTS_MYCODER_RUNTIME", "container")

	p := testProduct()
	base := map[string]any{
		"agents": map[string]any{
			"MyCoder": map[string]any{},
			"mycoder": map[string]any{},
		},
	}
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(base, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TESTPROD_CONFIG_AGENTS_MYCODER_RUNTIME")
	assert.Contains(t, err.Error(), "MyCoder")
	assert.Contains(t, err.Error(), "mycoder")

	agents := out["agents"].(map[string]any)
	assert.NotContains(t, agents["MyCoder"], "runtime")
	assert.NotContains(t, agents["mycoder"], "runtime")
}

// TestEnvOverlay_UnsetSchemaKeyCreatedWithoutWarning proves case 3: a path
// absent from base but valid per the product's schema is created SILENTLY --
// no warning -- because most schema keys are simply unset, and warning here
// would be constant noise.
func TestEnvOverlay_UnsetSchemaKeyCreatedWithoutWarning(t *testing.T) {
	t.Setenv("TESTPROD_CONFIG_DEFAULT_AGENT", "mycoder")

	p := testProduct("default_agent")
	base := map[string]any{}
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)
	assert.Equal(t, "mycoder", out["default_agent"])
}

// TestEnvOverlay_UnknownKeyWarnsAndCreates proves case 4: a path neither
// present in base nor known to the schema still gets created (the override
// is honored), but produces a warning so a typo doesn't silently vanish.
func TestEnvOverlay_UnknownKeyWarnsAndCreates(t *testing.T) {
	t.Setenv("TESTPROD_CONFIG_BOGUS", "x")

	p := testProduct("default_agent") // schema knows SOMETHING, just not this
	base := map[string]any{}
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)

	// Case 4's fallback keeps the token's ORIGINAL (un-lowercased) form: it
	// may be a brand-new user-chosen label the schema cannot enumerate, so
	// this must not force a casing convention onto it the way the
	// schema-matched branch does for a known field name.
	got, ok := out["BOGUS"]
	require.True(t, ok, "the override must still be applied even though it warns")
	assert.Equal(t, "x", got)
}

// TestEnvOverlay_BootstrapVarsExcluded proves a bootstrap/process-selection
// var (bare "TESTPROD_ROOT", missing the "_CONFIG_" segment EnvPrefix always
// carries) never becomes a layered override, structurally -- it simply
// doesn't match the prefix.
func TestEnvOverlay_BootstrapVarsExcluded(t *testing.T) {
	t.Setenv("TESTPROD_ROOT", "/some/path")
	t.Setenv("TESTPROD_CONFIG_DEFAULT_AGENT", "mycoder")

	p := testProduct("default_agent")
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	assert.NotContains(t, o.Env, "ROOT")
	assert.Len(t, o.Env, 1)

	out, err := p.ApplyOverrides(map[string]any{}, o)
	require.NoError(t, err)
	assert.NotContains(t, out, "root")
	assert.Equal(t, "mycoder", out["default_agent"])
}

// TestEnvOverlay_CoercesBoolIntAndList proves env values (always strings on
// arrival) are coerced to the type a schema-typed field expects: bool, int,
// and a comma-separated list.
func TestEnvOverlay_CoercesBoolIntAndList(t *testing.T) {
	t.Setenv("TESTPROD_CONFIG_ENABLED", "false")
	t.Setenv("TESTPROD_CONFIG_COUNT", "3")
	t.Setenv("TESTPROD_CONFIG_TAGS", "a,b,c")

	p := testProduct("enabled", "count", "tags")
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(map[string]any{}, o)
	require.NoError(t, err)

	assert.Equal(t, false, out["enabled"])
	assert.Equal(t, 3, out["count"])
	assert.Equal(t, []any{"a", "b", "c"}, out["tags"])
}

// TestEnvOverlay_ZeroAndOneStayIntegers is the regression guard for U108-F06:
// strconv.ParseBool accepts "0" and "1", so a bool-first type detection turned
// EVERY 0/1 integer override into a boolean and left the schema's five
// integer-typed keys with no way to express their smallest values at all.
// ctxloom's own `agent_turn_cap` is `{"type":"integer","minimum":1}`, so
// CTXLOOM_CONFIG_AGENT_TURN_CAP=1 -- the minimum legal value -- became `true`
// and then failed the whole layered yaml decode with nothing but a warning.
// Integers must win the 0/1 spellings; bools keep every alphabetic spelling.
func TestEnvOverlay_ZeroAndOneStayIntegers(t *testing.T) {
	t.Setenv("TESTPROD_CONFIG_AGENT_TURN_CAP", "1")
	t.Setenv("TESTPROD_CONFIG_COUNT", "0")
	t.Setenv("TESTPROD_CONFIG_ENABLED", "true")
	t.Setenv("TESTPROD_CONFIG_QUIET", "F")

	p := testProduct("agent_turn_cap", "count", "enabled", "quiet")
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(map[string]any{}, o)
	require.NoError(t, err)

	assert.Equal(t, 1, out["agent_turn_cap"], "an integer override of 1 must stay an int, not become true")
	assert.Equal(t, 0, out["count"], "an integer override of 0 must stay an int, not become false")
	assert.Equal(t, true, out["enabled"], "alphabetic bool spellings still coerce to bool")
	assert.Equal(t, false, out["quiet"], "single-letter bool spellings still coerce to bool")
}

// TestEnvOverlay_ExplicitFalseBeatsInheritedTrue proves an env override of
// "false" actually WINS over an inherited "true" from a lower layer --
// presence in the higher layer, not truthiness, decides (layerconfig's D3
// rule), so a naive "falsy values get skipped" bug would be caught here.
func TestEnvOverlay_ExplicitFalseBeatsInheritedTrue(t *testing.T) {
	t.Setenv("TESTPROD_CONFIG_ENABLED", "false")

	p := testProduct()
	base := map[string]any{"enabled": true}
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)
	assert.Equal(t, false, out["enabled"])
}

// TestFlagOverlay_UnchangedFlagDoesNotOverrideConfig proves a --config-set that was
// merely DECLARED on the FlagSet -- never actually given a value on this
// invocation -- contributes nothing, so a command that happens to register
// --config-set but receives none this run can never clobber a config value set by a
// lower layer.
func TestFlagOverlay_UnchangedFlagDoesNotOverrideConfig(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.StringArray(ConfigSetFlagName, nil, "")
	// deliberately not calling fs.Set with this flag

	p := testProduct("default_agent")
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)
	assert.Empty(t, o.Flags, "an unset --config-set must not appear in the raw override capture")

	base := map[string]any{"default_agent": "from-config-file"}
	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)
	assert.Equal(t, "from-config-file", out["default_agent"])
}

// TestConfigSetFlag_OverridesConfigKey is --config-set's positive case: a value DOES
// override, and wins over a same-named key already present in the file
// layers.
func TestConfigSetFlag_OverridesConfigKey(t *testing.T) {
	fs := configSetFlagSet(t, "llm.default=gemini")

	p := testProduct("llm")
	base := map[string]any{"llm": map[string]any{"default": "claude"}}
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)
	llm, ok := out["llm"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gemini", llm["default"])
}

// TestConfigSetFlag_DoesNotCollideWithCommandFlags is THE REGRESSION GUARD for the
// break acceptance testing caught: a command's OWN flags (--runtime on
// `agent set`/`container_cmd`, --workspace on `run`/`acp`/`map`, --version on
// `bundle`, --hooks on `manage`, ...) happen to share a NAME with a real
// top-level config key. Before --config-set existed, ReadOverrides scanned every
// CHANGED flag on the invoked command by name, so e.g. `ctxloom agent set
// coder --runtime container` silently overwrote the PROJECT's top-level
// `runtime`. Now ReadOverrides looks at NOTHING but --config-set, so these flags
// being present and changed on the SAME FlagSet must have zero effect on the
// resolved config -- covers a scalar collision (runtime, workspace), a
// bool-typed collision (version), and an OBJECT-typed collision (hooks,
// where a scalar assignment would otherwise have produced a type-invalid
// config).
func TestConfigSetFlag_DoesNotCollideWithCommandFlags(t *testing.T) {
	fs := pflag.NewFlagSet("agent set", pflag.ContinueOnError)
	fs.StringArray(ConfigSetFlagName, nil, "")
	fs.String("runtime", "", "")
	fs.String("workspace", "", "")
	fs.Bool("version", false, "")
	fs.String("hooks", "", "")
	require.NoError(t, fs.Set("runtime", "container"))
	require.NoError(t, fs.Set("workspace", "worktree"))
	require.NoError(t, fs.Set("version", "true"))
	require.NoError(t, fs.Set("hooks", "something"))
	// Deliberately NOT touching --config-set at all: this invocation gives zero
	// config overrides, only ordinary command flags.

	p := testProduct("runtime", "workspace", "version", "hooks")
	base := map[string]any{
		"runtime":   "host",
		"workspace": "main",
		"version":   6,
		"hooks":     map[string]any{"pretooluse": []any{}},
	}
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)
	assert.Empty(t, o.Flags, "only --config-set may contribute; a same-named command flag must never be scanned at all")

	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)
	assert.Equal(t, "host", out["runtime"], "the command's own --runtime flag must not clobber config runtime")
	assert.Equal(t, "main", out["workspace"], "the command's own --workspace flag must not clobber config workspace")
	assert.Equal(t, 6, out["version"], "the command's own --version flag must not clobber config version")
	assert.Equal(t, map[string]any{"pretooluse": []any{}}, out["hooks"],
		"the command's own --hooks flag must not stomp the object-typed hooks section with a scalar")
}

// TestConfigSetFlag_CreatesCaseSensitiveKeyAsTyped proves the env/--config-set case
// asymmetry (see confload's package doc): a shell destroys an env var name's
// case before this package ever sees it, but --config-set's VALUE preserves
// whatever case the user actually typed on the command line -- so, unlike
// env, --config-set can mint a brand-new case-sensitive key. The schema recognizes
// agents.<any label>.runtime (a dynamic map, exactly like the real ctxloom
// schema's `agents` section), so this also exercises the schema-fallback
// path (case 3) with a wildcard level in the middle of the path, not just
// the no-schema-knowledge fallback (case 4).
func TestConfigSetFlag_CreatesCaseSensitiveKeyAsTyped(t *testing.T) {
	fs := configSetFlagSet(t, "agents.MyCoder.runtime=container")

	p := Product{
		Name: "testprod", DirName: ".testprod", FileName: "config.yaml", EnvPrefix: "TESTPROD_CONFIG_",
		KnownPath: func(path []string) bool {
			return len(path) == 3 && path[0] == "agents" && path[2] == "runtime"
		},
	}
	base := map[string]any{}
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)

	agents, ok := out["agents"].(map[string]any)
	require.True(t, ok)
	mycoder, ok := agents["MyCoder"].(map[string]any)
	require.True(t, ok, "must create the label using the EXACT case typed (\"MyCoder\"), not lower-cased (\"mycoder\")")
	assert.Equal(t, "container", mycoder["runtime"])
}

// TestConfigSetFlag_ResolvesCaseInsensitivelyToExistingKey is
// TestConfigSetFlag_CreatesCaseSensitiveKeyAsTyped's counterpart: when the label
// ALREADY EXISTS (however it happens to be spelled), --config-set still resolves to
// it case-insensitively and adopts its casing, exactly like env does -- the
// divergence is only about what happens when there is NO existing match.
func TestConfigSetFlag_ResolvesCaseInsensitivelyToExistingKey(t *testing.T) {
	fs := configSetFlagSet(t, "agents.mycoder.runtime=container")

	p := testProduct()
	base := map[string]any{
		"agents": map[string]any{
			"MyCoder": map[string]any{"engine": "claude-code"},
		},
	}
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)

	agents, ok := out["agents"].(map[string]any)
	require.True(t, ok)
	mycoder, ok := agents["MyCoder"].(map[string]any)
	require.True(t, ok, "must adopt the EXISTING key's casing (\"MyCoder\"), not the typed one (\"mycoder\")")
	assert.Equal(t, "container", mycoder["runtime"])
	assert.Equal(t, "claude-code", mycoder["engine"], "sibling key from the file layer must survive")
}

// TestConfigSetFlag_AmbiguousCaseIsError mirrors TestEnvOverlay_AmbiguousCaseIsError
// for --config-set: a path that could refer to more than one existing key errors
// out naming the flag and both candidates, rather than guessing.
func TestConfigSetFlag_AmbiguousCaseIsError(t *testing.T) {
	fs := configSetFlagSet(t, "agents.mycoder.runtime=container")

	p := testProduct()
	base := map[string]any{
		"agents": map[string]any{
			"MyCoder": map[string]any{},
			"mycoder": map[string]any{},
		},
	}
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(base, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agents.mycoder.runtime")
	assert.Contains(t, err.Error(), "MyCoder")
	assert.Contains(t, err.Error(), "mycoder")

	agents := out["agents"].(map[string]any)
	assert.NotContains(t, agents["MyCoder"], "runtime")
	assert.NotContains(t, agents["mycoder"], "runtime")
}

// TestConfigSetFlag_MalformedIsError proves a --config-set with no "=" (or an empty path)
// is a reported error naming the offending argument, never silently dropped.
func TestConfigSetFlag_MalformedIsError(t *testing.T) {
	fs := configSetFlagSet(t, "no-equals-sign-here", "=empty-path")

	p := testProduct()
	_, err := p.ReadOverrides(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-equals-sign-here")
	assert.Contains(t, err.Error(), "=empty-path")
}

// TestConfigSetFlag_CoercesBoolIntAndList mirrors TestEnvOverlay_CoercesBoolIntAndList
// for --config-set: it reuses the identical coercion (coerceEnvValue), so bool, int,
// and comma-separated list values all resolve to their real Go type, not a
// plain string.
func TestConfigSetFlag_CoercesBoolIntAndList(t *testing.T) {
	fs := configSetFlagSet(t, "enabled=false", "count=3", "tags=a,b,c")

	p := testProduct("enabled", "count", "tags")
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(map[string]any{}, o)
	require.NoError(t, err)

	assert.Equal(t, false, out["enabled"])
	assert.Equal(t, 3, out["count"])
	assert.Equal(t, []any{"a", "b", "c"}, out["tags"])
}

// TestConfigSetFlag_UnknownKeyWarnsAndCreates mirrors TestEnvOverlay_UnknownKeyWarnsAndCreates
// for --config-set: since --config-set is NOW its own dedicated namespace (exactly like
// EnvPrefix is for env -- see the package doc), an unrecognized path warns
// and still applies, symmetric with env. This is the behavior that used to
// live on ordinary command flags (and had to be suppressed there, because
// ordinary flags are NOT a dedicated override namespace); --config-set restores it
// safely.
func TestConfigSetFlag_UnknownKeyWarnsAndCreates(t *testing.T) {
	fs := configSetFlagSet(t, "bogus=x")

	p := testProduct("default_agent") // schema knows SOMETHING, just not this
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)

	out, err := p.ApplyOverrides(map[string]any{}, o)
	require.NoError(t, err)
	got, ok := out["bogus"]
	require.True(t, ok, "the override must still be applied even though it warns")
	assert.Equal(t, "x", got)
}

// TestConfload_SecondProductReusesPattern proves the pattern is DRY across
// products: a second, taskloom-shaped Product (.taskloom / TASKLOOM_CONFIG_)
// exercises the identical full chain independently of the first product's
// env vars and config paths.
func TestConfload_SecondProductReusesPattern(t *testing.T) {
	dir := t.TempDir()
	homePath := filepath.Join(dir, "taskloom-home.yaml")
	projectPath := filepath.Join(dir, "taskloom-project.yaml")
	writeFile(t, homePath, "store: home-store\n")
	writeFile(t, projectPath, "store: project-store\n")

	t.Setenv("TASKLOOM_CONFIG_STORE", "env-store")
	// Cross-contamination guard: a ctxloom-prefixed var must never leak into
	// the taskloom product's resolution.
	t.Setenv("CTXLOOM_CONFIG_STORE", "should-never-appear")

	taskloom := Product{
		Name:      "taskloom",
		DirName:   ".taskloom",
		FileName:  "config.yaml",
		EnvPrefix: "TASKLOOM_CONFIG_",
	}
	assert.Equal(t, filepath.Join(dir, ".taskloom", "config.yaml"), taskloom.HomeConfigPath(dir))

	o, err := taskloom.ReadOverrides(nil)
	require.NoError(t, err)

	result, err := taskloom.Load(Sources{HomePath: homePath, ProjectPath: projectPath}, o)
	require.NoError(t, err)
	assert.Equal(t, "env-store", result["store"])
}

// TestOverrides_Stamp_ChangesWithContent proves Stamp is sensitive to both
// the env and cli override content, and stable (equal) for identical content
// -- the property internal/config's ambientStamp folding depends on.
func TestOverrides_Stamp_ChangesWithContent(t *testing.T) {
	empty := Overrides{}
	withEnv := Overrides{Env: map[string]any{"FOO": "bar"}}
	withEnvAgain := Overrides{Env: map[string]any{"FOO": "bar"}}
	withDifferentEnv := Overrides{Env: map[string]any{"FOO": "baz"}}

	assert.NotEqual(t, empty.Stamp(), withEnv.Stamp())
	assert.Equal(t, withEnv.Stamp(), withEnvAgain.Stamp())
	assert.NotEqual(t, withEnv.Stamp(), withDifferentEnv.Stamp())
}
