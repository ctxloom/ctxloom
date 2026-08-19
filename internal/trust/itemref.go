package trust

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// The item-selector GRAMMAR: the "#<kind>/<name>" half of a reference, and
// the recognizers for spellings the reference grammar no longer accepts.
//
// It lives HERE, in the package that owns Ref and BundleRef, rather than in
// operations, because the delivery pipeline (bundles.Pipeline) must judge a
// selector exactly as a `ctxloom bundle trust` mutation does. Two parsers
// would be two addressing schemes, and an item approved under one spelling
// would be gated under another.

// IsRetiredBuiltinSpelling reports whether ask is written as "builtin:<name>",
// the one bundle-reference spelling NOTHING in this system still mints: a
// builtin bundle's identity comes from BuiltinRef, and no lockfile, resolved
// profile or assembly identity carries this prefix.
//
// It is therefore the only spelling the LOAD path may refuse outright. The
// load path is handed self-contained identities that are still authored today
// — a lockfile's "<url>@bundles/<path>", a resolved profile's
// "ctxloom:local@bundles/<name>" — and refusing those would withhold the
// content they address.
//
// The literal is inlined rather than named: a constant invites reuse, and a
// retired spelling must not spread to a new call site. It lives in THIS
// package because it is the one package the builtin-literal sweep exempts.
func IsRetiredBuiltinSpelling(ask string) bool {
	return strings.HasPrefix(ask, "builtin:")
}

// IsRetiredAskSpelling reports whether ask, TYPED BY A HUMAN, carries a scheme
// marker belonging to a reference spelling the grammar no longer accepts. Such
// a token must FAIL CLOSED: a user who types a retired spelling needs to be
// told so, never silently downgraded to a bare-name search that resolves to
// something else or to "not found". Those are different faults and they
// deserve different messages.
//
// The set is remote.IsSelfContainedRef's list (ctxloom:local@,
// ctxloom:companion@, git@, any "://") plus IsRetiredBuiltinSpelling. It is
// deliberately WIDER than the load path's: at a surface where a human types a
// reference, the pipeline's own identity spellings are retired input, while on
// the load path the same strings are live identities a reader stamped.
func IsRetiredAskSpelling(ask string) bool {
	return IsRetiredBuiltinSpelling(ask) || remote.IsSelfContainedRef(ask)
}

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
