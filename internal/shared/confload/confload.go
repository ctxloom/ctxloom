// Package confload is the shared config-loading pattern every binary in the
// ctxloom family (ctxloom, taskloom, ltk) uses to close the full precedence
// chain layerconfig documents but does not itself resolve:
//
//	home config file  <  project config file  <  ENV VARS  <  --set FLAGS
//
// confload owns stages 2b/2c/2d of layerconfig's two-stage model (see that
// package's doc): given the already-merged home<project file layers (a
// caller-supplied map, built however that product needs), it resolves
// environment-variable and --set overrides against it and returns the fully
// layered result.
//
// # Product
//
// A Product names one binary's on-disk and environment conventions (config
// directory name, config file name, env-var prefix) plus an optional schema
// hook (KnownPath) used to distinguish a legitimate-but-unset config key from
// an unrecognized one when an override targets a path base doesn't already
// have (see the four resolution outcomes documented on resolvePath).
// confload itself knows nothing about any product's actual schema shape —
// KnownPath is the caller's own predicate, so this package stays free of
// ctxloom-specific (or taskloom-specific, or...) types.
//
// # CLI overrides: --set is the ONLY source, never a command's own flags
//
// ReadOverrides does NOT look at a *pflag.FlagSet's flags in general — only
// at SetFlagName ("set"), a repeatable flag of "<dotted.path>=<value>"
// entries. Env has a dedicated namespace via EnvPrefix (CTXLOOM_CONFIG_,
// never colliding with an ordinary flag); a cobra command's OWN flag pool
// has no equivalent, so opportunistically treating every CHANGED flag's NAME
// as a candidate config path (a prior revision of this package did exactly
// that) silently coupled every current and FUTURE flag name to the config
// schema — confirmed in production: `ctxloom agent set coder --runtime
// container` clobbered the project's top-level `runtime` key, and
// `--format json` on a structured-output command printed a warning line
// into what a script expected to be pure JSON, because `--format` and
// `--bundle` happened to resolve as "unrecognized config key, setting it
// anyway". --set gives flags the same kind of dedicated, unambiguous
// namespace EnvPrefix gives env vars — see ReadOverrides' doc for the full
// account.
//
// # Env vs. --set: case handling deliberately diverges
//
// A shell destroys an env var NAME's case before any Go code ever sees it
// (CTXLOOM_CONFIG_AGENTS_MYCODER_RUNTIME cannot say whether the source was
// MyCoder, mycoder, or MYCODER); a --set VALUE never goes through a shell's
// environment-variable-NAME rules, so it preserves whatever case the user
// actually typed on the command line. Both sources resolve identically when
// the target ALREADY EXISTS or is a fixed, canonically-cased schema field —
// case-insensitive match, adopting the existing/canonical casing (see
// resolvePath's cases 1-3). They diverge only when nothing existing OR fixed
// covers the segment (a dynamic, wildcard-accepting level — an agent label,
// an LLM config label — or truly nothing at all): env falls back to
// whatever case the shell happened to hand it (not a meaningful signal,
// since env var names are conventionally SCREAMING_SNAKE regardless of the
// "real" key's spelling); --set falls back to EXACTLY what the user typed,
// which IS meaningful and lets --set do something env fundamentally
// cannot: mint a brand-new case-sensitive key, e.g. `--set
// agents.MyCoder.runtime=container` creating an `agents.MyCoder` entry, or
// `--set llm.configs.big.env.GEMINI_API_KEY=...` a case-sensitive
// `GEMINI_API_KEY` inside an LLM backend's env passthrough. See
// resolvePath's preserveTypedCase parameter for the mechanics.
//
// # Overrides are captured once, resolved on every load
//
// ReadOverrides scans the process environment and a *pflag.FlagSet's --set
// values ONCE (typically from the root command's PersistentPreRun, before
// any config file is even read) and returns an Overrides value carrying the
// RAW, UNRESOLVED name->value pairs — an env var's name still has its
// EnvPrefix stripped but is not yet split into path tokens, and a --set
// entry's dotted path is not yet split either. This is deliberate: resolving
// a token sequence like AGENTS_MYCODER_RUNTIME into a config path requires
// matching case-insensitively against whatever keys the CURRENTLY-LOADING
// base actually has (see resolvePath) — and different Load calls in the same
// process can target genuinely different bases (a worktree's own
// .ctxloom/config.yaml via WithAppDir has its own key casing, independent of
// the ambient project's). Baking resolution into a one-time snapshot would
// get every load after the first wrong. So ReadOverrides is cheap and called
// once; the actual resolution (Load / ApplyOverrides) re-runs, against the
// live base, on every load — exactly like the file layers themselves are
// re-read on every load.
//
// # Config files are never decoded via viper
//
// This package (and internal/config's own file-layer loading) reads config
// FILES with yaml.Unmarshal only, never viper: viper lowercases every map
// key it decodes (confirmed empirically — MyCoder becomes mycoder,
// GEMINI_API_KEY becomes gemini_api_key), which is silently catastrophic for
// a case-sensitive pass-through map like an LLM backend's `env` block (see
// ctxloom commit 26f96c7, the regression this constraint exists to prevent: a
// backend's `env: {GEMINI_API_KEY: ...}` reached the launched process as
// `gemini_api_key` and the engine never saw its credential).
// TestConfig_EnvMapKeyCasePreserved (internal/config) is the end-to-end guard
// for this on ctxloom's own adoption of this package. This package does not
// use viper AT ALL: --set's values arrive from pflag as plain strings
// (StringArray), coerced by the same coerceEnvValue env values use, so there
// is no typed-pflag-value problem for viper to solve here either.
//
// # Process boundaries: env crosses, flags do not
//
// Env vars are inherited by child processes, so CTXLOOM_CONFIG_* overrides
// set for a ctxloom invocation are also visible to any taskloom/ltk/engine
// process it spawns (each honoring its OWN EnvPrefix, so they never collide).
// A --set FLAG IS NOT inherited: one given to `ctxloom run` cannot reach a
// spawned taskloom/ltk/engine's own config — that child parses its own argv,
// which never contains the parent's flags. This is inherent to how flags
// work (there is no ambient channel to carry them) and is treated as
// correct: a flag is scoped to the single invocation that declared it.
package confload

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/layerconfig"
)

// Product identifies one ctxloom-family binary's config conventions.
//
//   - Name is used as clidiag's "prog" for any warning EnvOverlay/FlagOverlay
//     emit (e.g. "taskloom: warning: ...").
//   - DirName/FileName are the conventional per-user/per-project config
//     location (e.g. ".ctxloom"/"config.yaml"); HomeConfigPath is a small
//     helper for callers that want to build a Sources from a home directory.
//   - EnvPrefix is the literal prefix an override env var must start with
//     (e.g. "CTXLOOM_CONFIG_", "TASKLOOM_CONFIG_" — deliberately never a bare
//     "CTXLOOM_": CTXLOOM_ROOT, CTXLOOM_PROJECT_ID, CTXLOOM_SESSION_HARP,
//     CTXLOOM_DEGRADED, CTXLOOM_VERBOSE etc. are bootstrap/process switches,
//     not layered config values — CTXLOOM_ROOT in particular SELECTS which
//     project config.yaml is even read, so it cannot also be a value sitting
//     "inside" the chain it determines the inputs of. A correctly-scoped
//     EnvPrefix keeps these out by construction, since none of them contain
//     the extra "_CONFIG_" segment; a Product misconfigured with a bare
//     family prefix would defeat this — that is a caller bug, not something
//     this package can detect for it.
//   - KnownPath, if set, reports whether path (case-insensitive segments,
//     already lower-cased) names a location the product's schema recognizes,
//     independent of whether base currently holds a value there. Nil is
//     treated as "no schema knowledge available": every override that
//     doesn't match an existing base key is conservatively treated as
//     unrecognized (always warned about).
type Product struct {
	Name      string
	DirName   string
	FileName  string
	EnvPrefix string
	KnownPath func(path []string) bool
}

// HomeConfigPath joins home/DirName/FileName — the conventional per-user
// config file location for this product.
func (p Product) HomeConfigPath(home string) string {
	return filepath.Join(home, p.DirName, p.FileName)
}

// Sources is stage-1 bootstrap's output: WHICH files stage 2 (this package,
// plus layerconfig.Merge) reads and layers. Resolving these paths — walking
// up from cwd, an explicit root override, a home-directory fallback — is
// each product's own business (see layerconfig's package doc on why bootstrap
// and value-layering are deliberately separate concerns); confload only
// consumes the result. Either field may be empty, meaning that layer
// contributes nothing (mirrors layerconfig.Layer's nil-Values no-op).
type Sources struct {
	HomePath    string
	ProjectPath string
}

// Overrides is the process-wide env/CLI override capture ReadOverrides
// produces — see the package doc's "Overrides are captured once, resolved on
// every load" section. Both fields are RAW (unresolved against any
// particular base): Env.Values is keyed by an override env var's name with
// EnvPrefix stripped (e.g. "AGENTS_MYCODER_RUNTIME"); Flags.Values is keyed
// by the pflag name that changed (e.g. "agents.mycoder.runtime" or
// "default-agent"). The zero Overrides{} is a legitimate "no overrides" value
// (both Values nil), matching layerconfig.Layer's own nil-Values no-op.
type Overrides struct {
	Env   layerconfig.Layer
	Flags layerconfig.Layer
}

// Load is the full four-layer convenience entry point: read HomePath and
// ProjectPath (plain case-preserving YAML — see the ABSOLUTE CONSTRAINT
// above), deep-merge them (layerconfig.Merge; project beats home), then
// resolve o's overrides against the result (ApplyOverrides), and return the
// fully-layered raw map.
//
// Load does NOT validate against any schema or unmarshal into a typed
// struct — each product owns its own schema and Go type, so that stays the
// caller's job on the returned map. A product whose file layers need their
// own per-layer processing before merging (ctxloom's upgrade pipeline +
// per-layer schema validation, see internal/config's loadLayeredConfig) skips
// Load and calls ApplyOverrides directly against its own already-merged base
// instead — ApplyOverrides is independently exported for exactly that
// reason.
func (p Product) Load(src Sources, o Overrides) (map[string]any, error) {
	var layers []layerconfig.Layer

	homeValues, err := readYAMLFile(src.HomePath)
	if err != nil {
		return nil, err
	}
	if homeValues != nil {
		layers = append(layers, layerconfig.Layer{Name: "home", Values: homeValues})
	}

	projectValues, err := readYAMLFile(src.ProjectPath)
	if err != nil {
		return nil, err
	}
	if projectValues != nil {
		layers = append(layers, layerconfig.Layer{Name: "project", Values: projectValues})
	}

	base := layerconfig.Merge(layers...)
	return p.ApplyOverrides(base, o)
}

// readYAMLFile decodes path into a presence-tracked map[string]any via
// yaml.Unmarshal — never viper (see the package doc's ABSOLUTE CONSTRAINT). A
// missing file is not an error (config files are optional); it returns (nil,
// nil), which Load treats as "this layer contributes nothing", exactly
// mirroring layerconfig.Layer's own nil-Values no-op.
func readYAMLFile(path string) (map[string]any, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("confload: read %s: %w", path, err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("confload: parse %s: %w", path, err)
	}
	return values, nil
}
