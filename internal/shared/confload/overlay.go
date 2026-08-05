package confload

import (
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	kmaps "github.com/knadh/koanf/maps"
	kenv "github.com/knadh/koanf/providers/env/v2"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// ConfigSetFlagName is the repeatable persistent flag every CLI-override goes
// through: --config-set <dotted.path>=<value>. Named "config-set", not the
// generic "set", so a future subcommand's own verb-shaped flag (a plausible
// "set" for its own purposes) can never collide with it — the exact
// namespace collision this flag exists to prevent in the first place (see
// ReadOverrides' doc). "config-set" mirrors the CTXLOOM_CONFIG_ env prefix,
// so both override channels are named consistently and self-describing at a
// glance. See ReadOverrides' doc for why this package does not otherwise
// look at a FlagSet's contents at all.
const ConfigSetFlagName = "config-set"

// EnvPrefixSegment is the segment a Product's EnvPrefix must contain, and the
// whole mechanism by which bootstrap/process-selection variables are kept out
// of the layered config chain. CTXLOOM_ROOT, CTXLOOM_PROJECT_ID,
// CTXLOOM_SESSION_HARP, CTXLOOM_DEGRADED, CTXLOOM_VERBOSE and their
// per-product equivalents are switches that select WHICH config is read; none
// of them carries this segment, so a prefix that does carries no risk of
// matching one.
//
// The Product doc spends eleven lines establishing that invariant. It is
// checkable in one, and ReadOverrides checks it: leaving the enforcement to
// the caller made the single most consequential misconfiguration this package
// admits — a bare family prefix like "CTXLOOM_" — silent, and its symptom
// (CTXLOOM_ROOT appearing as a config value named "root") would surface far
// from its cause.
const EnvPrefixSegment = "_CONFIG_"

// OverrideSource distinguishes the two override channels for a Product's
// ScopeAllows hook. The distinction is REACH, not merely syntax: env is
// inherited by every child process this one spawns (including a delegated
// engine an agent drives); --config-set crosses no process boundary at all,
// scoped to the one invocation that declared it. See Product.ScopeAllows.
type OverrideSource uint8

const (
	SourceEnv OverrideSource = iota
	SourceFlag
)

// String names s for a diagnostic.
func (s OverrideSource) String() string {
	switch s {
	case SourceEnv:
		return "env"
	case SourceFlag:
		return "flag"
	default:
		return "unknown"
	}
}

// ScopeViolationError is what ApplyOverrides returns (joined alongside any
// other override error) when Product.ScopeAllows answers false for a
// resolved override path. It is a distinct type — not a bare fmt.Errorf —
// so a caller with its own per-kind diagnostics (ctxloom's decodeMergedLayers
// classifies this as WarnKindLayerScope, distinctly from an ambiguous
// override's plainer error) can tell the two apart via errors.As instead of
// string-matching the message.
type ScopeViolationError struct {
	Source  OverrideSource
	Path    []string
	Why     string
	Display string // the raw override's display form (env var name / --config-set entry)
}

func (e *ScopeViolationError) Error() string {
	return fmt.Sprintf("%s: %s may not set %q — %s", e.Display, e.Source, strings.Join(e.Path, "."), e.Why)
}

// ReadOverrides captures the process's env/CLI overrides ONCE — see the
// package doc's "Overrides are captured once, resolved on every load"
// section. It does NOT resolve anything against a config base; that happens
// later, per load, in ApplyOverrides/Load.
//
// Env is built by koanf's env provider (github.com/knadh/koanf/providers/
// env/v2), which scans os.Environ() for vars starting with p.EnvPrefix and
// hands each surviving "name=value" pair through a TransformFunc that strips
// EnvPrefix and coerces the value (coerceEnvValue), stored keyed by the
// stripped name (e.g. CTXLOOM_CONFIG_AGENTS_MYCODER_RUNTIME becomes key
// "AGENTS_MYCODER_RUNTIME"). Bootstrap vars (CTXLOOM_ROOT, CTXLOOM_DEGRADED,
// ...) are excluded structurally, not by name: a correctly-scoped EnvPrefix
// always carries an extra "_CONFIG_" segment none of them contain (see the
// package doc's Product field comment). The provider is given an empty
// delim, so it returns the flat, un-nested map this stage wants — path
// resolution against a specific base happens later, in ApplyOverrides.
//
// # Flags: --config-set is the ONLY source, deliberately
//
// Flags is built EXCLUSIVELY from fs's --config-set values (nil fs, or an fs with no
// --config-set flag registered, is valid — a caller with no CLI overrides to
// contribute returns an empty Flags map, not an error). This package used
// to scan fs.VisitAll for every CHANGED flag on the invoked command and treat
// each one's own NAME as a candidate config path. That is unsound: a
// cobra.Command's FlagSet is that command's entire business-flag namespace
// (--format, --bundle, --sig, --runtime, --workspace, --version, --hooks,
// --agents, --mcp, --profiles, --llm, ...), most of which exists for reasons
// that have nothing to do with config, and several of which happen to share a
// NAME with a real top-level config key. `ctxloom agent set coder --runtime
// container` would have silently overwritten the PROJECT's top-level
// `runtime`; a structured `--format json` invocation would print a stray
// warning into what a script expects to be pure JSON (both confirmed by
// acceptance testing). Env has a dedicated namespace via EnvPrefix; flags
// never had an equivalent one, so opportunistic name-matching coupled every
// current and FUTURE flag name to the config schema by accident. --config-set gives
// flags the same kind of dedicated namespace EnvPrefix gives env vars: only
// something explicitly passed through --config-set is ever a config override.
//
// Each --config-set value must be "<path>=<value>"; a missing "=" or an empty path
// is a reported error (joined across every malformed entry), not silently
// dropped. The value is coerced exactly like an env var's (coerceEnvValue) —
// bool, then int, then a comma-separated list, else a plain string.
func (p Product) ReadOverrides(fs *pflag.FlagSet) (Overrides, error) {
	if !strings.Contains(p.EnvPrefix, EnvPrefixSegment) {
		// Scanning with this prefix would sweep the product's bootstrap and
		// process-selection vars into the config chain (see EnvPrefixSegment).
		// Refuse the scan entirely rather than layering them in: an override
		// channel that cannot be trusted to exclude CTXLOOM_ROOT is worse than
		// no override channel, since CTXLOOM_ROOT SELECTS which config file is
		// read and so cannot also be a value inside it.
		return Overrides{}, fmt.Errorf(
			"confload: product %q has EnvPrefix %q, which does not contain %q — every env override was skipped, because that prefix would also match this product's bootstrap variables",
			p.Name, p.EnvPrefix, EnvPrefixSegment)
	}

	envProvider := kenv.Provider("", kenv.Opt{
		Prefix: p.EnvPrefix,
		TransformFunc: func(name, raw string) (string, any) {
			suffix := strings.TrimPrefix(name, p.EnvPrefix)
			if suffix == "" {
				// An empty TransformFunc key return tells the provider to
				// omit the entry entirely -- mirrors the manual scan's own
				// `if suffix == "" { continue }`.
				return "", nil
			}
			return suffix, coerceEnvValue(raw)
		},
	})
	envValues, err := envProvider.Read()
	if err != nil {
		// The env provider's Read() has no I/O path that can fail (it only
		// walks os.Environ() in memory) -- guarded rather than ignored.
		return Overrides{}, fmt.Errorf("confload: read env overrides: %w", err)
	}

	flagValues := map[string]any{}
	var errs []error
	// A FlagSet with no --config-set flag registered is a caller with nothing
	// to contribute, and is not an error. That is decided by Lookup, NOT by
	// GetStringArray's error: GetStringArray fails for three distinct reasons
	// (no such flag, the flag exists with a different TYPE, and its value
	// failed to decode), and treating all three as "nothing to contribute"
	// silently discarded EVERY CLI override on the invocation for the two
	// causes that are real faults. Registering --config-set as a StringSlice
	// rather than a StringArray, for instance, would have removed --config-set
	// from the product entirely while every test still passed.
	if fs != nil && fs.Lookup(ConfigSetFlagName) != nil {
		entries, getErr := fs.GetStringArray(ConfigSetFlagName)
		if getErr != nil {
			errs = append(errs, fmt.Errorf("--%s is registered but its values are unreadable, so no CLI config override was applied: %w",
				ConfigSetFlagName, getErr))
		}
		for _, entry := range entries {
			path, raw, ok := strings.Cut(entry, "=")
			if !ok {
				errs = append(errs, fmt.Errorf("--%s %q: expected <path>=<value>", ConfigSetFlagName, entry))
				continue
			}
			if path == "" {
				errs = append(errs, fmt.Errorf("--%s %q: empty path before \"=\"", ConfigSetFlagName, entry))
				continue
			}
			flagValues[path] = coerceEnvValue(raw)
		}
	}

	return Overrides{Env: envValues, Flags: flagValues}, errors.Join(errs...)
}

// Stamp returns a cheap, deterministic identity for o's raw content — changes
// whenever any override name or value changes. It exists so a memoized
// config load (internal/config's ambientStamp) can fold it in alongside a
// config file's own mtime+size stat: an ambient memo built BEFORE overrides
// were installed (or before they changed) must not be served forever just
// because no file changed — see the package's consuming caller for the full
// rationale. The zero Overrides{} stamps as "env:|cli:", a stable constant a
// caller can compare against to detect "no overrides installed at all".
func (o Overrides) Stamp() string {
	return "env:" + stampFlat(o.Env) + "|cli:" + stampFlat(o.Flags)
}

// stampFlat renders a flat map deterministically (sorted keys) for Stamp.
func stampFlat(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%v;", k, m[k])
	}
	return b.String()
}

// ApplyOverrides resolves o's Env then Flags layers against base (env first,
// so a flag can still beat an env-set value — the documented precedence),
// returning the fully-layered result. This is the resolution step Load
// performs internally; it is exported directly for a product (like ctxloom)
// whose file layers need their own per-layer processing — upgrade pipeline,
// per-layer schema validation — before merging, and so builds its own base
// instead of going through Sources/Load.
//
// # Path resolution
//
// Each raw override name (an env var's EnvPrefix-stripped suffix, split on
// "_"; a --config-set path, split on ".") is resolved against base by resolvePath —
// see its doc for the full four-outcome algorithm (unique match / ambiguous /
// unset-but-schema-valid / unknown). This is policy koanf has no part in:
// koanf's job ends at "here is a value at a path"; deciding WHICH path a
// case-mangled env name or user-typed --config-set path resolves to is entirely
// ours.
//
// # Env vs. --config-set: case handling deliberately diverges
//
// A shell destroys an env var NAME's case before any Go code ever sees it
// (CT_AGENTS_MYCODER_RUNTIME cannot say whether the source was MyCoder,
// mycoder, or MYCODER); a --config-set VALUE never goes through the shell's
// environment-variable-name rules at all, so it preserves whatever case the
// user actually typed. Case 1 (matches an existing key) and case 2
// (ambiguous) treat both sources identically — resolution is always
// case-insensitive against what base already has, adopting base's own
// casing. Case 4 (nothing recognizes it) is where they diverge: an env
// override falls back to the token's own (env-shell) case, which is
// whatever the user happened to type in their shell's assignment and
// carries no special meaning; a --config-set override falls back to EXACTLY the
// case the user typed on the command line, which is therefore trustworthy
// enough to CREATE a brand-new case-sensitive key with — e.g. `--config-set
// agents.MyCoder.runtime=container` can mint a fresh `agents.MyCoder` entry,
// or `--config-set llm.configs.big.env.GEMINI_API_KEY=...` a fresh
// case-sensitive `GEMINI_API_KEY` inside an LLM backend's env passthrough —
// something no env var could ever do, since the var's OWN name has already
// lost that information before this package sees it. Case 3
// (schema-known-but-unset) is unaffected either way: a schema field name has
// exactly one canonical (lower_snake_case) spelling, which resolvePath
// always produces regardless of source.
//
// # Errors are non-fatal and partial
//
// An ambiguous override (case 2) does not abort the whole resolution:
// ApplyOverrides resolves every OTHER override normally and returns the
// resulting layer WITH the ambiguous one's target simply absent, alongside a
// non-nil error (joined across every ambiguous override found, env and cli
// together) naming the variable/flag and its candidates. Callers follow this
// codebase's fault-tolerance convention: downgrade the error to a warning
// rather than fail startup over one bad override.
func (p Product) ApplyOverrides(base map[string]any, o Overrides) (map[string]any, error) {
	var errs []error

	// Both sources warn-and-create on case 4 (unrecognized): --config-set is a
	// DEDICATED namespace exactly like EnvPrefix is for env (see
	// ReadOverrides' doc) -- everything reaching either one already declared
	// itself a config override, so there is no more "this might just be an
	// unrelated flag" ambiguity to protect against.
	//
	// Both merge steps below use the package's plain Merge — NEVER
	// p.MergeLayers/p.MergeFunc — deliberately: an override is a PATCH (one
	// resolved path, one value), not a competing whole-document layer, and a
	// product's MergeFunc (ctxloom's agentBindingMergeFunc among them) is
	// written for the "whichever FILE layer names this whole binding wins"
	// question. Feeding a single-field flag override like `--config-set
	// agents.reviewer.permissions=bypass` through an atomic-replace merge
	// wiped out that agent's OTHER fields (profiles, engine, ...) entirely —
	// confirmed against a running binary while validating this seam — which
	// is not "the flag names a new binding for reviewer", it is "the flag
	// tweaks one field of the existing one". MergeFunc's atomic behavior
	// belongs solely to Product.MergeLayers' other caller (the FILE-layer
	// merge in Load / a caller like ctxloom's own decodeMergedLayers), never
	// here.
	envLayer, envErr := p.resolveRaw(base, o.Env, envTokens, p.envSourceName, false, SourceEnv)
	if envErr != nil {
		errs = append(errs, envErr)
	}
	merged, mergeErr := Merge(base, envLayer)
	if mergeErr != nil {
		return nil, mergeErr
	}
	base = merged

	flagLayer, flagErr := p.resolveRaw(base, o.Flags, setTokens, flagSourceName, true, SourceFlag)
	if flagErr != nil {
		errs = append(errs, flagErr)
	}
	merged, mergeErr = Merge(base, flagLayer)
	if mergeErr != nil {
		return nil, mergeErr
	}
	base = merged

	return base, errors.Join(errs...)
}

// envSourceName reconstructs an env var's original display name (with prefix)
// for diagnostics.
func (p Product) envSourceName(suffix string) string {
	return p.EnvPrefix + suffix
}

// flagSourceName reconstructs a --config-set entry's original display form for
// diagnostics.
func flagSourceName(path string) string {
	return "--" + ConfigSetFlagName + " " + path
}

// envTokens splits an env var's (already EnvPrefix-stripped) name into word
// tokens on "_".
func envTokens(suffix string) []string {
	return strings.Split(suffix, "_")
}

// setTokens splits a --config-set path into its dot-separated segments. Unlike
// envTokens (which splits a shell-cased NAME into words that may need
// regrouping to recover a multi-word key resolvePath's widest-match search
// handles), a --config-set path is already explicit about where one config level
// ends and the next begins — each "." IS one level, full stop. Feeding these
// through the same widest-match search as env tokens is still correct (a
// wider join essentially never matches a real key, so it always falls
// through to the single-segment width), just slightly redundant for this
// source; the shared algorithm is kept rather than a second one, since path
// length here is always small.
func setTokens(path string) []string {
	return strings.Split(path, ".")
}

// resolveRaw is the shared engine ApplyOverrides runs twice (once for env,
// once for --config-set): it walks raw in deterministic (sorted) key order,
// tokenizes each key via tokenize, resolves it against base via resolvePath,
// and places the (already-typed) value at the resolved path in the returned
// map. sourceName renders a raw key into the display form resolvePath's
// diagnostics (and clidiag warnings) use. preserveTypedCase is threaded
// straight through to resolvePath — see that function's doc for the env-vs-
// --config-set case divergence it controls.
//
// Every resolved (path, value) pair is collected into a FLAT map keyed by
// the path segments joined with delim, then unflattened in one pass via
// koanf/maps.Unflatten — the koanf-provided replacement for this package's
// former hand-rolled setPath helper (see delim's doc for why "." itself is
// never used as the join/split character here).
func (p Product) resolveRaw(base map[string]any, raw map[string]any, tokenize func(string) []string, sourceName func(string) string, preserveTypedCase bool, source OverrideSource) (map[string]any, error) {
	flat := map[string]any{}
	var errs []error

	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		tokens := tokenize(name)
		if len(tokens) == 0 {
			continue
		}
		display := sourceName(name)
		path, warn, err := p.resolvePath(base, tokens, display, preserveTypedCase)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if p.ScopeAllows != nil {
			if ok, why := p.ScopeAllows(source, path); !ok {
				// Dropped, not fatal — exactly like an ambiguous override
				// (case 2): every OTHER override still resolves normally, and
				// this one's target is simply absent from the returned layer.
				// A typed error (not a bare fmt.Errorf), so a caller can
				// classify this distinctly from every other override fault.
				errs = append(errs, &ScopeViolationError{Source: source, Path: path, Why: why, Display: display})
				continue
			}
		}
		if warn {
			clidiag.Warn(p.Name, "%s does not match any known config key (resolved as %s); setting it anyway",
				display, strings.Join(path, "."))
		}
		flat[strings.Join(path, delim)] = raw[name]
	}

	return kmaps.Unflatten(flat, delim), errors.Join(errs...)
}

// coerceEnvValue converts a raw environment/--config-set string into the Go value a
// schema-typed field expects: int (strconv.ParseInt base 10), then bool
// (strconv.ParseBool), then — if the string contains a comma — a
// []any of its (independently coerced) comma-separated, trimmed parts, and
// otherwise the string unchanged.
//
// INT IS TRIED BEFORE BOOL, and that order is load-bearing. strconv.ParseBool
// accepts "0" and "1" as well as the alphabetic spellings, so a bool-first
// order coerced EVERY 0/1 integer override into a boolean and left the
// integer-typed schema keys (agent_turn_cap among them, declared
// {"type":"integer","minimum":1}) with no way to express their smallest legal
// values at all — CTXLOOM_CONFIG_AGENT_TURN_CAP=1 became `true`, which then
// failed the layered yaml decode of the WHOLE config document with nothing but
// a warning. Int-first costs bools only the numeric spellings; every
// alphabetic one (true/false/t/f/T/F/TRUE/False/...) still reads as a bool, so
// booleans remain fully expressible while integers stop being corrupted.
//
// This is a policy decision entirely ours, not koanf's: koanf's own getters
// (Bool()/Int()/...) require the CALLER to already know which typed getter
// to ask for, which presupposes knowing the schema shape at the call site —
// exactly what this package cannot assume, since KnownPath is an optional,
// caller-supplied predicate and an override may target a path with no schema
// knowledge at all (case 4). coerceEnvValue instead does TYPE DETECTION from
// the raw string itself, uses strconv directly (not a swallow-on-failure
// typed getter) so a parse failure correctly falls through to the next
// candidate type instead of silently returning a zero value.
func coerceEnvValue(raw string) any {
	if i, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return int(i)
	}
	if b, err := strconv.ParseBool(raw); err == nil {
		return b
	}
	if strings.Contains(raw, ",") {
		parts := strings.Split(raw, ",")
		list := make([]any, len(parts))
		for i, part := range parts {
			list[i] = coerceEnvValue(strings.TrimSpace(part))
		}
		return list
	}
	return raw
}

// resolvePath is the resolution engine shared by both overlays (env and cli).
// tokens are the raw, already-split words from an override's source name
// (env-var suffix split on "_", flag name split on "."/"-"). sourceName is
// the original display name (env var or "--flag"), used only for
// diagnostics.
//
// It proceeds in two phases:
//
//  1. Base-guided descent: walk as far as tokens allow while base itself
//     names the next level. At each level, try consuming the WIDEST possible
//     prefix of the remaining tokens (all of them, then one fewer, ... down
//     to one) joined with "_" and compared, case-insensitively, against that
//     level's actual keys — widest first, so a multi-word key already
//     present in base (however it happens to be spelled) is preferred over
//     splitting it into several descent levels. A unique case-insensitive
//     match descends, adopting base's own casing for that segment (case 1).
//     More than one existing key folding to the same candidate is case 2:
//     resolution stops immediately with an error naming sourceName and every
//     colliding candidate.
//
//  2. Schema-guided fallback: once base can no longer guide the walk (a
//     level absent from base, or base ran out before tokens did), every way
//     of partitioning the REMAINING tokens into contiguous groups is tried,
//     FINEST first (most groups — one token per segment — before any joining
//     at all), lower-cased and appended to the already-resolved prefix, and
//     tested against p.KnownPath. The first schema-valid completion wins,
//     silently (case 3) — this is the common path: most schema keys are
//     simply unset.
//
//     Finest-first (not widest-first) is deliberate: a schema level that
//     accepts ANY key (additionalProperties — an agent label, an LLM config
//     label, ...) validates trivially for ANY candidate segment reaching it,
//     including one that greedily swallows tokens which actually belong to a
//     field NESTED inside that dynamic entry. E.g. for tokens
//     [agents, MyCoder, runtime], a widest-first search would accept
//     ["agents", "mycoder_runtime"] (misreading "MyCoder_runtime" as one
//     agent's own dynamic label) before ever trying the correct
//     ["agents", "mycoder", "runtime"] (an agent named "mycoder" with a
//     "runtime" field) — silently creating the wrong shape. Finest-first
//     tries the fully-split, most-specific interpretation FIRST, so a real
//     multi-word FIXED field name (e.g. isolation_base_containerfile, one
//     top-level property) is only matched once every finer split has
//     already failed to validate — which it always does for a genuinely
//     fixed (non-wildcard) property name, since "isolation" alone is not
//     itself a property there.
//
//     If no partition validates (or KnownPath is nil), the remaining tokens
//     fall back to one segment per token, exactly as originally split, and
//     the caller is told to warn (case 4) — the existing unknown-key
//     diagnostic machinery then reports it the same way a typo'd YAML key
//     would.
//
// preserveTypedCase controls one more env-vs-cli divergence WITHIN case 3
// (see the package doc's "Env vs. --config-set: case handling deliberately
// diverges" section for the full picture): when true, each partition's
// ORIGINAL-CASE candidate is tried against KnownPath before its lower-cased
// form. A wildcard schema level (additionalProperties — an agent label, an
// LLM config label) accepts a segment regardless of its case, so the
// original-case candidate validates there just as well as the lower-cased
// one and is preferred, preserving exactly what the user typed (e.g. --config-set
// agents.MyCoder.runtime=... keeps "MyCoder"). A FIXED schema property name
// only validates in its one canonical (lower_snake_case) spelling, so an
// original-case candidate naturally fails there and the lower-cased
// fallback still wins — preserveTypedCase never causes a genuine schema
// field name to be mis-cased. false (env's case) skips the original-case
// attempt entirely: an env var's token case is a shell-casing artifact
// carrying no user intent (env var names are conventionally
// SCREAMING_SNAKE regardless of the "real" key's spelling), so preferring
// it would create confidently-wrong-cased dynamic labels instead of the
// safe, neutral lower-cased default.
func (p Product) resolvePath(base map[string]any, tokens []string, sourceName string, preserveTypedCase bool) (path []string, warn bool, err error) {
	var resolved []string
	level := base
	remaining := tokens

	for level != nil && len(remaining) > 0 {
		key, next, consumed, ambiguous, matched := descendOneLevel(level, remaining)
		if len(ambiguous) > 1 {
			return nil, false, fmt.Errorf(
				"%s: ambiguous override -- config has both %s; cannot tell which was meant (env/flag names lose case information)",
				sourceName, strings.Join(quoteAll(ambiguous), " and "))
		}
		if !matched {
			break
		}
		resolved = append(resolved, key)
		remaining = remaining[consumed:]
		level = next
	}

	if len(remaining) == 0 {
		return resolved, false, nil
	}

	if path, ok := p.resolveBySchema(resolved, remaining, preserveTypedCase); ok {
		return path, false, nil
	}

	// Case 4: nothing recognized it. Fall back to one segment per token, in
	// the token's original (un-lowercased) form -- it may be a brand-new
	// user-chosen dynamic label the schema cannot enumerate, so this does not
	// force a casing convention onto it the way the schema-matched branch
	// does for a KNOWN field name.
	fallback := append([]string{}, resolved...)
	fallback = append(fallback, remaining...)
	return fallback, true, nil
}

// resolveBySchema is resolvePath's phase 2: once base can no longer guide the
// walk, try every partition of the remaining tokens against p.KnownPath and
// report the first schema-valid completion (case 3). ok is false when there is
// no schema knowledge to consult or nothing validated, which is what sends
// resolvePath to case 4.
//
// resolved is the prefix phase 1 already agreed on and is never re-examined;
// only remaining is partitioned. See resolvePath's doc for why the partitions
// arrive finest-first, and preserveTypedCase's paragraph there for why the
// original-case candidate is tried before the lower-cased one when the source
// is a --config-set path rather than an env var.
func (p Product) resolveBySchema(resolved, remaining []string, preserveTypedCase bool) ([]string, bool) {
	if p.KnownPath == nil {
		return nil, false
	}
	for _, groups := range partitionsBySegmentCountDesc(remaining) {
		if preserveTypedCase {
			original := append([]string{}, resolved...)
			for _, g := range groups {
				original = append(original, strings.Join(g, "_"))
			}
			if p.KnownPath(original) {
				return original, true
			}
		}
		lowered := append([]string{}, resolved...)
		for _, g := range groups {
			lowered = append(lowered, strings.ToLower(strings.Join(g, "_")))
		}
		if p.KnownPath(lowered) {
			return lowered, true
		}
	}
	return nil, false
}

// descendOneLevel finds level's own key for the start of remaining, trying
// the widest possible token prefix first (see resolvePath's doc). matched is
// false when no width found a unique case-insensitive key at this level
// (base cannot guide any further); ambiguous carries every colliding key
// spelling when a width found more than one.
func descendOneLevel(level map[string]any, remaining []string) (key string, next map[string]any, consumed int, ambiguous []string, matched bool) {
	for width := len(remaining); width >= 1; width-- {
		candidate := strings.Join(remaining[:width], "_")
		var hits []string
		for k := range level {
			if strings.EqualFold(k, candidate) {
				hits = append(hits, k)
			}
		}
		if len(hits) > 1 {
			sort.Strings(hits)
			return "", nil, 0, hits, false
		}
		if len(hits) == 1 {
			sub, _ := level[hits[0]].(map[string]any)
			return hits[0], sub, width, nil, true
		}
	}
	return "", nil, 0, nil, false
}

// maxPartitionTokens bounds partitionsBySegmentCountDesc's input, because the
// enumeration it performs is 2^(n-1) in n and n comes from a USER-SUPPLIED
// name: an env var CTXLOOM_CONFIG_A_B_C_... or a --config-set path splits into
// as many tokens as the user cares to type. Unbounded, the pre-sized
// make([]splitMask, 0, 1<<gaps) below turns a merely long name into an
// out-of-memory abort during config load — i.e. at startup, before the process
// can say anything useful — and past 64 tokens the shift itself overflows into
// a negative cap and panics in makeslice.
//
// 16 is measured against the schemas this predicate is asked about: the
// deepest path either product declares is 6 segments
// (profiles.definitions.*.fragments.*.priority), and the longest fixed
// multi-word property name contributes about 3 tokens, so 16 leaves roughly
// 2.5x headroom over any legitimate override while capping the enumeration at
// 2^15 = 32768 partitions.
//
// Beyond the bound the schema-guided search is skipped entirely and resolution
// falls through to resolvePath's case 4 — one segment per token, plus the
// unknown-key warning naming the variable. That is a strictly better outcome
// than an OOM, and it costs only this: a path longer than the bound can no
// longer be matched by JOINING tokens into a wildcard segment. Nothing shorter
// than the bound changes at all.
const maxPartitionTokens = 16

// partitionsBySegmentCountDesc returns every way of splitting tokens into
// contiguous, order-preserving groups, ordered by DESCENDING group count
// (i.e. finest splits — one token per segment — first, widest joins last).
// See resolvePath's doc for why finest-first, not widest-first, is required
// once a schema level with a wildcard (additionalProperties) is possible.
//
// Returns nil for an empty token list and for one longer than
// maxPartitionTokens — see that constant for why the bound exists and what
// exceeding it costs. A nil result makes resolvePath's schema-guided loop a
// no-op, so it lands on case 4.
func partitionsBySegmentCountDesc(tokens []string) [][][]string {
	n := len(tokens)
	if n == 0 || n > maxPartitionTokens {
		return nil
	}
	gaps := n - 1
	type splitMask struct {
		mask, popcount int
	}
	masks := make([]splitMask, 0, 1<<gaps)
	for m := 0; m < (1 << gaps); m++ {
		masks = append(masks, splitMask{m, bits.OnesCount(uint(m))})
	}
	sort.SliceStable(masks, func(i, j int) bool { return masks[i].popcount > masks[j].popcount })

	out := make([][][]string, 0, len(masks))
	for _, sm := range masks {
		var groups [][]string
		start := 0
		for i := 0; i < gaps; i++ {
			if sm.mask&(1<<i) != 0 {
				groups = append(groups, tokens[start:i+1])
				start = i + 1
			}
		}
		groups = append(groups, tokens[start:])
		out = append(out, groups)
	}
	return out
}

// quoteAll %q-quotes every string in ss, for building a readable "both X and
// Y" ambiguity message.
func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}
