package confload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/layerconfig"
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

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("d", "", "")
	require.NoError(t, fs.Set("d", "cli"))

	p := testProduct()
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)

	result, err := p.Load(Sources{HomePath: homePath, ProjectPath: projectPath}, o)
	require.NoError(t, err)

	assert.Equal(t, "home", result["a"], "layer with no override at any higher level stays home's")
	assert.Equal(t, "project", result["b"], "project beats home")
	assert.Equal(t, "env", result["c"], "env beats project")
	assert.Equal(t, "cli", result["d"], "cli beats env")
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

	assert.NotContains(t, o.Env.Values, "ROOT")
	assert.Len(t, o.Env.Values, 1)

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

// TestFlagOverlay_UnchangedFlagDoesNotOverrideConfig proves a flag that was
// merely DECLARED on the FlagSet -- never explicitly set on this invocation --
// contributes nothing, so a command registering a flag it doesn't use this
// run can never clobber a config value set by a lower layer.
func TestFlagOverlay_UnchangedFlagDoesNotOverrideConfig(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("default-agent", "unused-zero-value", "")
	// deliberately not calling fs.Set / fs.Parse with this flag

	p := testProduct("default_agent")
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)
	assert.Empty(t, o.Flags.Values, "an unchanged flag must not appear in the raw override capture")

	base := map[string]any{"default_agent": "from-config-file"}
	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)
	assert.Equal(t, "from-config-file", out["default_agent"])
}

// TestFlagOverlay_ChangedFlagOverridesConfig is FlagOverlay's positive
// counterpart: an actually-changed flag DOES override.
func TestFlagOverlay_ChangedFlagOverridesConfig(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("default-agent", "unused-zero-value", "")
	require.NoError(t, fs.Set("default-agent", "mycoder"))

	p := testProduct("default_agent")
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)

	base := map[string]any{"default_agent": "from-config-file"}
	out, err := p.ApplyOverrides(base, o)
	require.NoError(t, err)
	assert.Equal(t, "mycoder", out["default_agent"])
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
	withEnv := Overrides{Env: layerconfig.Layer{Name: "env", Values: map[string]any{"FOO": "bar"}}}
	withEnvAgain := Overrides{Env: layerconfig.Layer{Name: "env", Values: map[string]any{"FOO": "bar"}}}
	withDifferentEnv := Overrides{Env: layerconfig.Layer{Name: "env", Values: map[string]any{"FOO": "baz"}}}

	assert.NotEqual(t, empty.Stamp(), withEnv.Stamp())
	assert.Equal(t, withEnv.Stamp(), withEnvAgain.Stamp())
	assert.NotEqual(t, withEnv.Stamp(), withDifferentEnv.Stamp())
}
