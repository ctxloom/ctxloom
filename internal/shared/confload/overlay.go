package confload

import (
	"errors"
	"fmt"
	"math/bits"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/layerconfig"
)

// ReadOverrides captures the process's env/CLI overrides ONCE — see the
// package doc's "Overrides are captured once, resolved on every load"
// section. It does NOT resolve anything against a config base; that happens
// later, per load, in ApplyOverrides/Load.
//
// Env is built by scanning os.Environ() for vars starting with p.EnvPrefix:
// each is coerced (coerceEnvValue) and stored keyed by its name with
// EnvPrefix stripped (e.g. CTXLOOM_CONFIG_AGENTS_MYCODER_RUNTIME becomes key
// "AGENTS_MYCODER_RUNTIME"). Bootstrap vars (CTXLOOM_ROOT, CTXLOOM_DEGRADED,
// ...) are excluded structurally, not by name: a correctly-scoped EnvPrefix
// always carries an extra "_CONFIG_" segment none of them contain (see the
// package doc's Product field comment).
//
// Flags is built from fs (nil is valid — a caller with no CLI flags to
// contribute returns an empty Flags layer, not an error): only fs.Changed
// flags contribute, keyed by their own flag name, with values read via a
// viper instance BindPFlags'd to fs so each flag's own concrete type (bool,
// int, stringSlice, ...) survives instead of the raw flag.Value.String() this
// package would otherwise have to re-parse by hand. A flag merely DECLARED
// (its zero/default value, never explicitly set on this invocation)
// contributes nothing — this is the fix the project's viper research called
// out by name (an older viper release let a bound-but-unchanged flag's zero
// value win over an already-set config value); it is also simply what
// "override" has to mean.
func (p Product) ReadOverrides(fs *pflag.FlagSet) (Overrides, error) {
	envValues := map[string]any{}
	for _, kv := range os.Environ() {
		name, raw, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, p.EnvPrefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, p.EnvPrefix)
		if suffix == "" {
			continue
		}
		envValues[suffix] = coerceEnvValue(raw)
	}

	flagValues := map[string]any{}
	var err error
	if fs != nil {
		v := viper.New()
		if bindErr := v.BindPFlags(fs); bindErr != nil {
			err = fmt.Errorf("confload: bind cli flags: %w", bindErr)
		} else {
			fs.VisitAll(func(f *pflag.Flag) {
				if !f.Changed {
					return
				}
				flagValues[f.Name] = v.Get(f.Name)
			})
		}
	}

	return Overrides{
		Env:   layerconfig.Layer{Name: "env", Values: envValues},
		Flags: layerconfig.Layer{Name: "cli", Values: flagValues},
	}, err
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
	return "env:" + stampFlat(o.Env.Values) + "|cli:" + stampFlat(o.Flags.Values)
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
// "_"; a flag's own name, split on "." and "-") is resolved against base by
// resolvePath — see its doc for the full four-outcome algorithm (unique
// match / ambiguous / unset-but-schema-valid / unknown).
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

	envLayer, envErr := p.resolveRaw(base, o.Env, envTokens, p.envSourceName)
	if envErr != nil {
		errs = append(errs, envErr)
	}
	base = layerconfig.Merge(layerconfig.Layer{Name: "files", Values: base}, envLayer)

	flagLayer, flagErr := p.resolveRaw(base, o.Flags, flagTokens, flagSourceName)
	if flagErr != nil {
		errs = append(errs, flagErr)
	}
	base = layerconfig.Merge(layerconfig.Layer{Name: "files+env", Values: base}, flagLayer)

	return base, errors.Join(errs...)
}

// envSourceName reconstructs an env var's original display name (with prefix)
// for diagnostics.
func (p Product) envSourceName(suffix string) string {
	return p.EnvPrefix + suffix
}

// flagSourceName reconstructs a flag's original display name for
// diagnostics.
func flagSourceName(name string) string {
	return "--" + name
}

// envTokens splits an env var's (already EnvPrefix-stripped) name into word
// tokens on "_".
func envTokens(suffix string) []string {
	return strings.Split(suffix, "_")
}

// flagTokens splits a pflag flag name into word tokens on both "." (an
// explicit nesting separator, for a flag a command declares as e.g.
// "agents.mycoder.runtime") and "-" (pflag's own kebab-case word separator
// within one segment, e.g. "default-agent"). Both feed the same resolvePath
// grouping search envTokens' underscore-split tokens do, so "--default-agent"
// and "CTXLOOM_CONFIG_DEFAULT_AGENT" resolve identically.
func flagTokens(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == '-' })
}

// resolveRaw is the shared engine ApplyOverrides runs twice (once for env,
// once for cli): it walks raw.Values in deterministic (sorted) key order,
// tokenizes each key via tokenize, resolves it against base via resolvePath,
// and places the (already-typed) value at the resolved path in the returned
// layer. sourceName renders a raw key into the display form resolvePath's
// diagnostics (and clidiag warnings) use.
func (p Product) resolveRaw(base map[string]any, raw layerconfig.Layer, tokenize func(string) []string, sourceName func(string) string) (layerconfig.Layer, error) {
	out := map[string]any{}
	var errs []error

	names := make([]string, 0, len(raw.Values))
	for name := range raw.Values {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		tokens := tokenize(name)
		if len(tokens) == 0 {
			continue
		}
		display := sourceName(name)
		path, warn, err := p.resolvePath(base, tokens, display)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if warn {
			clidiag.Warn(p.Name, "%s does not match any known config key (resolved as %s); setting it anyway",
				display, strings.Join(path, "."))
		}
		setPath(out, path, raw.Values[name])
	}

	return layerconfig.Layer{Name: raw.Name, Values: out}, errors.Join(errs...)
}

// coerceEnvValue converts a raw environment string into the Go value a
// schema-typed field expects: bool (strconv.ParseBool), then int
// (strconv.ParseInt base 10), then — if the string contains a comma — a
// []any of its (independently coerced) comma-separated, trimmed parts, and
// otherwise the string unchanged.
//
// This uses strconv directly rather than viper's typed getters (GetBool,
// GetInt, ...) deliberately: those swallow a parse failure and silently
// return the type's zero value, which is exactly wrong for TYPE DETECTION —
// asking "is this a bool" via GetBool always says yes (possibly false). This
// package needs the parse itself to report success/failure to choose the
// right type at all.
func coerceEnvValue(raw string) any {
	if b, err := strconv.ParseBool(raw); err == nil {
		return b
	}
	if i, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return int(i)
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

// setPath writes value into out at the nested location path names, creating
// intermediate map[string]any levels as needed without disturbing sibling
// keys already present at any level.
func setPath(out map[string]any, path []string, value any) {
	m := out
	for i, seg := range path {
		if i == len(path)-1 {
			m[seg] = value
			return
		}
		next, ok := m[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[seg] = next
		}
		m = next
	}
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
//  2. Schema-guided fallback: once base can no longer guide the walk (a
//     level absent from base, or base ran out before tokens did), every way
//     of partitioning the REMAINING tokens into contiguous groups is tried,
//     fewest groups (widest joins) first, lower-cased and appended to the
//     already-resolved prefix, and tested against p.KnownPath. The first
//     schema-valid completion wins, silently (case 3) — this is the common
//     path: most schema keys are simply unset. If no partition validates (or
//     KnownPath is nil), the remaining tokens fall back to one segment per
//     token, exactly as originally split, and the caller is told to warn
//     (case 4) — the existing unknown-key diagnostic machinery then reports
//     it the same way a typo'd YAML key would.
func (p Product) resolvePath(base map[string]any, tokens []string, sourceName string) (path []string, warn bool, err error) {
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

	// Schema-guided fallback for whatever base couldn't resolve.
	if p.KnownPath != nil {
		for _, groups := range partitionsBySegmentCountAsc(remaining) {
			candidate := append([]string{}, resolved...)
			for _, g := range groups {
				candidate = append(candidate, strings.ToLower(strings.Join(g, "_")))
			}
			if p.KnownPath(candidate) {
				return candidate, false, nil
			}
		}
	}

	// Case 4: nothing recognized it. Fall back to one segment per token, in
	// the token's original (un-lowercased) form -- it may be a brand-new
	// user-chosen dynamic label the schema cannot enumerate, so this does not
	// force a casing convention onto it the way the schema-matched branch
	// above does for a KNOWN field name.
	fallback := append([]string{}, resolved...)
	fallback = append(fallback, remaining...)
	return fallback, true, nil
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

// partitionsBySegmentCountAsc returns every way of splitting tokens into
// contiguous, order-preserving groups, ordered by ASCENDING group count
// (i.e. widest joins — fewest groups — first). len(tokens) is expected to be
// small (a handful of underscore/dash-separated words), so the full
// 2^(n-1)-composition enumeration this does is negligible.
func partitionsBySegmentCountAsc(tokens []string) [][][]string {
	n := len(tokens)
	if n == 0 {
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
	sort.SliceStable(masks, func(i, j int) bool { return masks[i].popcount < masks[j].popcount })

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
