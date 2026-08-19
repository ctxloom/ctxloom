package trust

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// The item-ref GRAMMAR: turning "<bundle-ref>#<kind>/<name>" into the Ref the
// decision function keys on.
//
// It lives HERE, in the package that owns Ref, rather than in operations,
// because the delivery pipeline (bundles.Pipeline) must build the same Ref for
// the same string that a `ctxloom trust` mutation does. Two parsers would be two
// addressing schemes, and an item approved under one spelling would be gated
// under another.

// ParseItemRef splits an item ref "<bundle-ref>#<kind>/<name>" into the
// trust.Ref (repo, bundle path, kind, name, locality), the bundle ref to load
// content from, and any "@<commit>" provenance carried on the bundle ref.
func ParseItemRef(ref string) (tRef Ref, loadRef, version string, err error) {
	// Ingest boundary, and the sharpest one in the codebase: this ref arrives
	// from argv (`ctxloom bundle trust <ref>`), from an MCP argument, or from a gate
	// built over bundle-authored names, and its Bundle/Name components are
	// interpolated verbatim into the countersign preimage via countersignRef.
	// The bare-local fallback below accepts ANY token that carries no scheme
	// marker, so without this the grammar never gets a chance to object.
	ref = remote.NormalizeRef(ref)
	base, sel, found := strings.Cut(ref, "#")
	if !found || base == "" {
		return Ref{}, "", "", fmt.Errorf("trust ref %q missing #<kind>/<name> selector", ref)
	}
	kind, name, err := ParseSelector(sel)
	if err != nil {
		return Ref{}, "", "", fmt.Errorf("invalid trust ref %q: %w", ref, err)
	}

	if parsed, perr := remote.ParseReference(base); perr == nil {
		return Ref{
			RepoURL: parsed.URL,
			Bundle:  parsed.Path,
			Kind:    kind,
			Name:    name,
			IsLocal: parsed.IsLocal,
			// IsCompanion rides the SAME parse, from the same reference
			// grammar, so the decision function's companion step can never be
			// reached by a ref that did not parse as ctxloom:companion@<bin>.
			// Copied rather than re-derived from the URL string: one parser,
			// one answer.
			IsCompanion: parsed.IsCompanion,
		}, base, parsed.ContentVersion, nil
	}

	// base failed to parse as a canonical/local ref. A builtin bundle's
	// RETIRED (pre-U3b-3) source-ref spelling, "builtin:<name>", is recognized
	// explicitly here (never falls through to the local guess below) so a
	// caller still holding that old spelling — this ask grammar is unchanged
	// until it is retired in its own slice — gets IsBuiltin rather than
	// IsLocal, distinct identities in the trust store. The literal is inlined
	// rather than named: the only thing that ever minted it,
	// localFSReader.readBundle's builtin stamp, mints trust.BuiltinRef
	// directly now, so this is purely a recognizer for input already in the
	// wild, not a producer's contract.
	if bundle, ok := strings.CutPrefix(base, "builtin:"); ok {
		return Ref{Bundle: bundle, Kind: kind, Name: name, IsBuiltin: true}, base, "", nil
	}

	// base is still unrecognized. A genuinely local bundle is referenced by a
	// bare name carrying NO scheme marker at all (e.g. "my-tools", "lang/go") —
	// that is the only case this may still resolve to local. Anything that
	// LOOKS like an attempted canonical/local/builtin ref (a URL scheme, a
	// git@ prefix, or the ctxloom:local@ prefix) but failed to parse must NOT
	// be silently downgraded to "local": that would let an unrecognized or
	// malformed source ref bypass the trust gate entirely (the fail-open bug
	// this fixes — a seeded remote bundle whose canonical ref somehow fails to
	// parse must fail CLOSED, not open). Every caller of ParseItemRef
	// already treats an error as fail-closed (the content/exec gates withhold,
	// TrustStamper stamps pending, the CLI mutations refuse the operation), so
	// erroring here is safe in every call site.
	if remote.IsSelfContainedRef(base) {
		return Ref{}, "", "", fmt.Errorf(
			"trust ref %q: %q is not a valid canonical or ctxloom:local reference "+
				"(and not a builtin source) — refusing to treat an unrecognized source as local", ref, base)
	}

	// base is a bare token with no scheme marker → a plain local bundle name.
	return Ref{Bundle: base, Kind: kind, Name: name, IsLocal: true}, base, "", nil
}

// The fail-closed/fail-open boundary above — "does this string carry a marker
// saying it was INTENDED as a scheme-qualified reference?" — is
// remote.IsSelfContainedRef, and it is the ONLY copy of this list, in the
// package that owns the ref grammar. Two independent copies of this check
// previously drifted: one recognised any "://" but not ctxloom:companion@, so
// a malformed companion ref was downgraded to a first-party local bundle name
// and auto-trusted at step 3 — trusted MORE than a well-formed one. A single
// list cannot drift from itself.

// ParseSelector parses a "<kind>/<name>" selector (the part after "#").
func ParseSelector(sel string) (ItemKind, string, error) {
	kindDir, name, found := strings.Cut(sel, "/")
	if !found || name == "" {
		return "", "", fmt.Errorf("selector %q must be <kind>/<name>", sel)
	}
	switch kindDir {
	case "fragments":
		return KindFragment, name, nil
	case "commands", "prompts":
		// "commands" is the current spelling (the CLI list emits #commands/<name>);
		// "prompts" is the legacy alias from the prompt→skill rename before it.
		// Both map to KindPrompt so the stored key (KindPrompt.Dir() ==
		// "prompts"), the assembly-time content gate, and existing acceptances
		// stay valid — the content lives in bundle.Commands, which the hash
		// helpers read under KindPrompt.
		//
		// NOTE: "skills" is deliberately NOT an alias here. Before the
		// skill→command rename, "skills" meant this same command kind; it now
		// frees it for the TRUE Agent Skill kind (KindSkill, below) instead
		// — the CLI/review surface already moved off "#skills/" entirely,
		// so nothing production still relies on the old meaning.
		return KindPrompt, name, nil
	case "mcp":
		return KindMCP, name, nil
	case "hooks":
		// name is the hook's "<event>/<index>" identity (carries an inner slash).
		return KindHook, name, nil
	case "skills":
		return KindSkill, name, nil
	default:
		return "", "", fmt.Errorf("unknown item kind %q (want fragments|commands|mcp|hooks|skills)", kindDir)
	}
}
