// Package confload is the shared config-loading pattern every binary in the
// ctxloom family (ctxloom, taskloom, ltk) uses to close the full precedence
// chain
//
//	home config file  <  project config file  <  ENV VARS  <  --config-set FLAGS
//
// confload owns the whole chain: reading and deep-merging the file layers
// (via koanf, see Merge) and resolving environment-variable and --config-set
// overrides against the merged result (ApplyOverrides).
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
// # CLI overrides: --config-set is the ONLY source, never a command's own flags
//
// ReadOverrides does NOT look at a *pflag.FlagSet's flags in general — only
// at ConfigSetFlagName ("config-set"), a repeatable flag of "<dotted.path>=<value>"
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
// anyway". --config-set gives flags the same kind of dedicated, unambiguous
// namespace EnvPrefix gives env vars — see ReadOverrides' doc for the full
// account.
//
// # Env vs. --config-set: case handling deliberately diverges
//
// A shell destroys an env var NAME's case before any Go code ever sees it
// (CTXLOOM_CONFIG_AGENTS_MYCODER_RUNTIME cannot say whether the source was
// MyCoder, mycoder, or MYCODER); a --config-set VALUE never goes through a shell's
// environment-variable-NAME rules, so it preserves whatever case the user
// actually typed on the command line. Both sources resolve identically when
// the target ALREADY EXISTS or is a fixed, canonically-cased schema field —
// case-insensitive match, adopting the existing/canonical casing (see
// resolvePath's cases 1-3). They diverge only when nothing existing OR fixed
// covers the segment (a dynamic, wildcard-accepting level — an agent label,
// an LLM config label — or truly nothing at all): env falls back to
// whatever case the shell happened to hand it (not a meaningful signal,
// since env var names are conventionally SCREAMING_SNAKE regardless of the
// "real" key's spelling); --config-set falls back to EXACTLY what the user typed,
// which IS meaningful and lets --config-set do something env fundamentally
// cannot: mint a brand-new case-sensitive key, e.g. `--config-set
// agents.MyCoder.runtime=container` creating an `agents.MyCoder` entry, or
// `--config-set llm.configs.big.env.GEMINI_API_KEY=...` a case-sensitive
// `GEMINI_API_KEY` inside an LLM backend's env passthrough. See
// resolvePath's preserveTypedCase parameter for the mechanics.
//
// This case-matching POLICY is entirely ours — koanf has no opinion on it,
// and none of the koanf-provided pieces (Merge's deep-merge, the env
// provider's prefix scan) touch it. koanf gives a value at a path; deciding
// WHICH path a shell-mangled or user-typed name resolves to is resolvePath's
// job, unchanged by the move to koanf underneath.
//
// # Overrides are captured once, resolved on every load
//
// ReadOverrides scans the process environment and a *pflag.FlagSet's --config-set
// values ONCE (typically from the root command's PersistentPreRun, before
// any config file is even read) and returns an Overrides value carrying the
// RAW, UNRESOLVED name->value pairs — an env var's name still has its
// EnvPrefix stripped but is not yet split into path tokens, and a --config-set
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
// # Config files and merging are never routed through viper
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
// for this on ctxloom's own adoption of this package. koanf (github.com/
// knadh/koanf/v2) is deliberately CASE-SENSITIVE by design — its own docs
// state `app.server.port` and `APP.SERVER.port` are different keys — which
// is exactly why it is used here for the merge machinery viper cannot be
// trusted with; the earlier hand-rolled layerconfig package (deep-merge,
// list-replace, explicit-zero-beats-inheritance) is gone, replaced by Merge
// below, which is backed by koanf's own default merge (map[string]any
// deep-merge; anything else replaces) — the same semantics, koanf's
// implementation. Two koanf-specific pitfalls are pinned by tests in this
// package: koanf.Raw() round-trips values in a way that can surprise callers
// (see TestKoanf_IntStaysInt — this package always reads results back via
// Unmarshal, never Raw()), and koanf's default "." path delimiter would
// shatter a literally-dotted config key such as an LLM label "gpt-4.1" (see
// TestKoanf_DottedMapKeySurvives — this package uses delim, an ASCII Unit
// Separator no real config key can contain, everywhere a koanf instance or
// path-join is used).
//
// # Process boundaries: env crosses, flags do not
//
// Env vars are inherited by child processes, so CTXLOOM_CONFIG_* overrides
// set for a ctxloom invocation are also visible to any taskloom/ltk/engine
// process it spawns (each honoring its OWN EnvPrefix, so they never collide).
// A --config-set FLAG IS NOT inherited: one given to `ctxloom run` cannot reach a
// spawned taskloom/ltk/engine's own config — that child parses its own argv,
// which never contains the parent's flags. This is inherent to how flags
// work (there is no ambient channel to carry them) and is treated as
// correct: a flag is scoped to the single invocation that declared it.
package confload

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/knadh/koanf/providers/confmap"
	koanf "github.com/knadh/koanf/v2"
	"gopkg.in/yaml.v3"
)

// delim is the path delimiter every koanf.Koanf instance and koanf/maps call
// in this package uses — deliberately NOT "." (koanf's own default). "." is
// a legal character inside a real config key: an LLM config can legitimately
// be labeled "gpt-4.1", and koanf's default delimiter would silently split
// that one key into two path segments ("gpt-4" then "1") the instant
// anything in this package flattened or unflattened a path through it. "\x1f"
// (ASCII Unit Separator) is a non-printable control character no YAML author,
// env-var segment, or --config-set path segment could ever type as part of a real
// key, so it can never collide. See TestKoanf_DottedMapKeySurvives.
const delim = "\x1f"

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

// Sources is stage-1 bootstrap's output: WHICH files stage 2 (this package)
// reads and layers. Resolving these paths — walking up from cwd, an explicit
// root override, a home-directory fallback — is each product's own business;
// confload only consumes the result. Either field may be empty, meaning that
// layer contributes nothing.
type Sources struct {
	HomePath    string
	ProjectPath string
}

// Overrides is the process-wide env/CLI override capture ReadOverrides
// produces — see the package doc's "Overrides are captured once, resolved on
// every load" section. Both fields are RAW (unresolved against any
// particular base): Env is keyed by an override env var's name with
// EnvPrefix stripped (e.g. "AGENTS_MYCODER_RUNTIME"); Flags is keyed by a
// --config-set entry's dotted path (e.g. "agents.mycoder.runtime"). The zero
// Overrides{} is a legitimate "no overrides" value (both maps nil).
type Overrides struct {
	Env   map[string]any
	Flags map[string]any
}

// Load is the full four-layer convenience entry point: read HomePath and
// ProjectPath (plain case-preserving YAML — see the package doc's "Config
// files and merging are never routed through viper" section), deep-merge
// them (Merge; project beats home), then resolve o's overrides against the
// result (ApplyOverrides), and return the fully-layered raw map.
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
	var layers []map[string]any

	homeValues, homePresent, err := readYAMLFile(src.HomePath)
	if err != nil {
		return nil, err
	}
	p.warnIfKeyless(src.HomePath, homeValues, homePresent)
	if homeValues != nil {
		layers = append(layers, homeValues)
	}

	projectValues, projectPresent, err := readYAMLFile(src.ProjectPath)
	if err != nil {
		return nil, err
	}
	p.warnIfKeyless(src.ProjectPath, projectValues, projectPresent)
	if projectValues != nil {
		layers = append(layers, projectValues)
	}

	base, err := Merge(layers...)
	if err != nil {
		return nil, err
	}
	return p.ApplyOverrides(base, o)
}

// warnIfKeyless reports the one state readYAMLFile's presence flag exists to
// expose: a config file that IS there but contributes nothing — zero bytes,
// whitespace, entirely commented out, a bare `---`, or a literal `null`. All of
// those decode to a nil map with no error, indistinguishable from "no file" at
// the value level, so before the presence flag existed a user who commented out
// their whole config.yaml (or whose file got truncated) silently ran on
// defaults. It is a warning, not an error: an empty config file is legal, just
// almost never intended.
//
// The two genuinely-silent states are deliberately excluded — a layer that was
// never configured (empty path) and an optional file that simply is not there
// are both normal, and warning about them would make this line noise everyone
// learns to skip past.
func (p Product) warnIfKeyless(path string, values map[string]any, present bool) {
	if !present || len(values) > 0 {
		return
	}
	clidiag.Warn(p.Name, "%s exists but defines no configuration keys (empty, or entirely commented out); "+
		"this layer contributes nothing", path)
}

// readYAMLFile decodes path into a presence-tracked map[string]any via
// yaml.Unmarshal — never viper (see the package doc). A missing file is not
// an error (config files are optional); it returns (nil, false, nil), which
// Load treats as "this layer contributes nothing".
//
// present distinguishes "there is a file here" from "there is not", which the
// returned map alone cannot: an empty, whitespace-only, comment-only, `---`-only
// or `null` document all decode to a nil map with a nil error, exactly like an
// absent file. See warnIfKeyless for why that distinction has to survive the
// return.
func readYAMLFile(path string) (values map[string]any, present bool, err error) {
	if path == "" {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("confload: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, true, fmt.Errorf("confload: parse %s: %w", path, err)
	}
	return values, true, nil
}

// Merge deep-merges layers in ASCENDING precedence order: layers[0] is the
// weakest (e.g. home), layers[len-1] the strongest (e.g. CLI arguments).
// Typical call: Merge(home, project, env, cli).
//
// Merge rules, applied key by key, recursively — this is koanf's own default
// merge behavior (github.com/knadh/koanf/v2, via its confmap provider),
// deliberately never overridden with a custom koanf.WithMergeFunc:
//
//   - A key present in a higher layer always wins over the same key in a
//     lower layer, REGARDLESS of its value — including its zero value. This
//     is the explicit-zero-beats-inheritance rule: a project setting
//     `feature: false` beats an inherited `feature: true` from home, because
//     presence — not truthiness — is what koanf's merge consults. See
//     TestKoanf_ExplicitZeroBeatsInheritance.
//   - A key present in a lower layer but ABSENT from every higher layer is
//     inherited unchanged.
//   - When the same key holds a map[string]any in BOTH layers, the maps are
//     merged recursively (deep merge) rather than one replacing the other —
//     so a project can override one nested field without restating its
//     siblings.
//   - Any other type — including slices/lists — is replaced wholesale by the
//     higher layer's value, never concatenated or otherwise combined. A
//     project's list REPLACES home's list. This is deliberate: concatenation
//     would make it impossible for a project to shorten or remove an
//     inherited entry without inventing a negation syntax. A project that
//     wants "home's list plus one more" restates the whole list. See
//     TestKoanf_ListsReplaceNotConcat.
//
// Merge never mutates its inputs; each layer is copied into koanf before
// being merged in, so every returned map (including nested ones) is
// independent of the caller's originals.
//
// Merge reads the result back out via Unmarshal, never koanf's Raw()/All():
// Raw() round-trips a value in a way that can surprise a caller expecting an
// int to stay an int (see TestKoanf_IntStaysInt) — Unmarshal into a plain
// map[string]any does not have this problem and is used exclusively here.
func Merge(layers ...map[string]any) (map[string]any, error) {
	k := koanf.New(delim)
	for _, layer := range layers {
		if len(layer) == 0 {
			continue
		}
		// confmap.Provider with an empty delim treats layer as an
		// already-nested map (never unflattened) and hands it to k.Load with
		// a nil Parser — believed unable to fail for an in-memory provider
		// (U108-F01/F02: the prior "continue"/empty-map handling of this
		// branch treated that belief as certainty, silently dropping the
		// WHOLE layer — and every layer after it, since the loop kept
		// going — on the day it turns out to be wrong). Propagate instead:
		// an impossible state should fail loudly, not disappear.
		if err := k.Load(confmap.Provider(layer, ""), nil); err != nil {
			return nil, fmt.Errorf("confload: loading a config layer: %w", err)
		}
	}

	var out map[string]any
	if err := k.Unmarshal("", &out); err != nil {
		// Unmarshal into map[string]any believed unable to fail for content
		// that came from confmap.Provider (no decode-hook type mismatch is
		// possible against an interface{}-valued target) — same reasoning,
		// same fix: propagate rather than fabricate an empty-map result that
		// discards every successfully-loaded layer.
		return nil, fmt.Errorf("confload: unmarshaling merged layers: %w", err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}
