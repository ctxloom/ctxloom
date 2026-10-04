package trust

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/refuri"
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
// the one bundle-reference spelling NOTHING in this system still mints: the
// bundles that used to be embedded in the binary now arrive as ctxloom's own
// companion loadout, and no lockfile, resolved profile or assembly identity
// carries this prefix.
//
// It is therefore the only spelling the LOAD path may refuse outright. The
// load path is handed self-contained identities that are still authored today
// — an authored "<url>@bundles/<path>", a resolved profile's
// "ctxloom:local@bundles/<name>" — and refusing those would withhold the
// content they address.
//
// The literal is inlined rather than named: a constant invites reuse, and a
// retired spelling must not spread to a new call site. It lives in THIS
// package because it is the one package the builtin-literal sweep exempts.
func IsRetiredBuiltinSpelling(ask string) bool {
	return strings.HasPrefix(ask, "builtin:")
}

// IsRetiredAtEntry is the ENTRY-BOUNDARY guard: call it only where a human
// types a reference, never on the load path. It reports whether ask carries
// a scheme marker belonging to a reference spelling the grammar no longer
// accepts. Such a token must FAIL CLOSED: a user who types a retired spelling
// needs to be told so, never silently downgraded to a bare-name search that
// resolves to something else or to "not found". Those are different faults
// and they deserve different messages.
//
// The set is refuri.IsSelfContainedRef's plus IsRetiredBuiltinSpelling. It is
// deliberately WIDER than the load path's: at a surface where a human types a
// reference, the pipeline's own identity spellings are retired input, while on
// the load path the same strings are live identities a reader stamped.
func IsRetiredAtEntry(ask string) bool {
	return IsRetiredBuiltinSpelling(ask) || refuri.IsSelfContainedRef(ask)
}

// FormatSelector renders the "<kind>/<name>" selector (the part after "#")
// that ParseSelector reads back to exactly (kind, name): the ONE selector
// renderer, so minting cannot drift from parsing. A command is written
// under its current spelling, "commands/"; ItemKind.Dir stays the STORED
// directory ("prompts") that persisted trust keys use, and is not a
// selector spelling.
func FormatSelector(kind ItemKind, name string) string {
	dir := kind.Dir()
	if kind == KindPrompt {
		dir = "commands"
	}
	return dir + "/" + name
}

// ParseSelector parses a "<kind>/<name>" selector (the part after "#").
//
// The name it returns is NORMALISED (refuri.NormalizeRef): a caller holding the
// parsed value has every reason to use it and none to reach back for the raw
// selector, which is the text a control byte rides in on. net/url cuts the
// fragment before its own control-byte check, so for the item half of a
// reference this parse is the only place the cleaning can live.
func ParseSelector(sel string) (ItemKind, string, error) {
	kindDir, name, found := strings.Cut(refuri.NormalizeRef(sel), "/")
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
