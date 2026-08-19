package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Interactive trust review/marking surface (trust rework, TR4). The `show -i`
// flags render an item's (or bundle's) effective trust + source through the TR3
// resolver and then OFFER an explicit mutation — viewing never trusts. Every
// surface is TTY-gated by the caller (isInteractiveTerminal): with a piped/
// redirected stdout the content path is untouched and no trust UI is emitted, so
// `show` output stays byte-for-byte identical for scripts. All trust UI is
// written to the COMMAND's error writer (cmd.ErrOrStderr — os.Stderr in
// production) so it never mingles with the content on stdout, and so the one
// surface whose job is telling a user what they are about to trust can be
// asserted from a command harness.
//
// The actual mutations reuse the single plumbing path (runItemTrust /
// runItemReject → operations.Set*), and the interactive read reuses the single
// shared stdin reader via promptLine / promptYesNo (prompt.go) — no second
// bufio.Reader is created (ctxloom-code-08-002).

// itemTrustChoice is the parsed decision from the `[t]rust / [r]eject / skip`
// menu. It is split out of the raw terminal read so the action it drives is unit
// testable without a TTY (see TestApplyItemTrustChoice_*).
type itemTrustChoice int

const (
	itemTrustSkip itemTrustChoice = iota
	itemTrustGrant
	itemTrustReject
)

// parseItemTrustChoice maps a raw interactive answer to an action. Only an
// explicit "t"/"r" trusts or rejects; anything else — including the empty
// line, "s", or an EOF-truncated read — is a skip, because viewing must never
// mutate trust.
//
// The letters are `ctxloom bundle trust` and `ctxloom bundle reject`, the same
// two this menu drives: every surface that offers this decision offers it in
// one vocabulary, so what a reviewer learns here is what they can type.
func parseItemTrustChoice(answer string) itemTrustChoice {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "t":
		return itemTrustGrant
	case "r":
		return itemTrustReject
	default:
		return itemTrustSkip
	}
}

// applyItemTrustChoice performs the chosen mutation through the same operations
// the standalone `ctxloom bundle trust`/`bundle reject` commands use, so there
// is exactly one mutation path and the on-disk result is identical. Skip is a
// no-op: viewing an item never trusts it. cfg- and cmd-injectable so a test can
// drive each branch against a temp project and assert the written store.
func applyItemTrustChoice(cmd *cobra.Command, cfg *config.Config, ref string, choice itemTrustChoice) error {
	switch choice {
	case itemTrustGrant:
		return runItemTrust(cmd, cfg, ref)
	case itemTrustReject:
		return runItemReject(cmd, cfg, ref)
	default:
		return nil // skip — viewing never trusts
	}
}

// stampedTrust renders an effective-trust result as "trusted|withheld (source:
// X)" for the human review surfaces.
func stampedTrust(res operations.EffectiveTrustResult) string {
	state := "withheld"
	if res.Trusted() {
		state = "trusted"
	}
	return fmt.Sprintf("%s (source: %s)", state, res.Source)
}

// offerItemTrust is the TTY-gated interactive trust review for `fragment/prompt
// show -i`. The item CONTENT has already been written to stdout by the caller;
// this prints the effective trust + source and the action menu to stderr, then
// performs the explicit choice. A prompt whose answer could not be read is a
// skip either way; a read FAULT additionally says so (warnPromptFault).
func offerItemTrust(cmd *cobra.Command, cfg *config.Config, ref string) error {
	res := operations.NewTrustStamper(cfg).ForRef(ref)
	fmt.Fprintf(cmd.ErrOrStderr(), "\nEffective trust: %s\n", stampedTrust(res))
	answer, err := promptLine("[t]rust / [r]eject / skip? ")
	if err != nil {
		warnPromptFault(cmd, err)
		return nil // unread prompt → skip; viewing never trusts
	}
	return applyItemTrustChoice(cmd, cfg, ref, parseItemTrustChoice(answer))
}

// offerBundleTrust is the TTY-gated interactive trust review for `bundle show
// <name> -i`. It prints every item's effective trust + source — fragments,
// prompts, MCP servers, AND bundle hooks — resolved through one shared
// TrustStamper (store + registry read once, each bundle materialized once) to
// stderr, then offers an explicit per-hook [t]rust/[r]eject action. The
// whole-bundle posture offer is gone (trust-simplify: postures no longer
// affect exposure; the review porcelain's accept-bundle is hash-bound and
// arrives in slice 2). The bundle body is already on stdout; only an explicit
// action writes.
func offerBundleTrust(cmd *cobra.Command, cfg *config.Config, name string, bundle *bundles.Bundle) error {
	stamper := operations.NewTrustStamper(cfg)
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "\nPer-item effective trust for bundle %q:\n", name)
	for _, n := range bundle.FragmentNames() {
		printBundleItemTrust(w, stamper, name, trust.KindFragment, n)
	}
	for _, n := range bundle.PromptNames() {
		printBundleItemTrust(w, stamper, name, trust.KindPrompt, n)
	}
	for _, n := range bundle.MCPNames() {
		printBundleItemTrust(w, stamper, name, trust.KindMCP, n)
	}
	for _, e := range bundle.Hooks.Entries() {
		printBundleHookTrust(w, stamper, name, e)
	}

	// Bundle hooks are arbitrary-command executables — offer an explicit
	// per-hook [t]rust/[r]eject (a content-pinned acceptance / sticky
	// rejection), routed through the same path as `ctxloom bundle trust|reject
	// <bundle>#hooks/<event>/<index>`. Viewing never trusts.
	return offerBundleHookTrust(cmd, cfg, name, bundle)
}

// offerBundleHookTrust walks the bundle's hooks in canonical identity order and
// offers an explicit [t]rust/[r]eject/skip action per hook, applying the
// choice through the shared applyItemTrustChoice path so the on-disk result is
// identical to `ctxloom bundle trust|reject <bundle>#hooks/<event>/<index>`. An
// unread answer stops the walk and leaves every remaining hook exactly as it
// was — viewing never trusts — and the number left unreviewed is reported, so
// an abandoned review is never mistaken for a completed one. A hookless bundle
// is a no-op (no prompt emitted).
func offerBundleHookTrust(cmd *cobra.Command, cfg *config.Config, name string, bundle *bundles.Bundle) error {
	entries := bundle.Hooks.Entries()
	if len(entries) == 0 {
		return nil
	}
	w := cmd.ErrOrStderr()
	fmt.Fprintln(w, "\nBundle hooks are executable surfaces — trust or reject each:")
	for i, e := range entries {
		ref := name + "#hooks/" + e.ID()
		answer, err := promptLine(fmt.Sprintf("  hooks/%s — [t]rust / [r]eject / skip? ", e.ID()))
		if err != nil {
			// The walk stops here, and every hook from this one on stays
			// exactly as it was — viewing never trusts. What was missing is the
			// COUNT: a user who asked to review every executable surface in a
			// bundle, answered once, and saw the command finish had no way to
			// tell a completed review from an abandoned one.
			warnPromptFault(cmd, err)
			fmt.Fprintf(w, "  %d hook(s) not reviewed; each keeps its current trust.\n", len(entries)-i)
			return nil
		}
		if err := applyItemTrustChoice(cmd, cfg, ref, parseItemTrustChoice(answer)); err != nil {
			return err
		}
	}
	return nil
}

// warnPromptFault reports a trust prompt whose answer was never read.
//
// It distinguishes the two events the caller cannot: io.EOF is the user
// deliberately ending input (Ctrl-D), which IS an answer and stays silent,
// while any other error is a terminal read fault that must not be presented as
// a choice the user made. Neither grants anything — the skip posture is
// identical — so this changes what the user is TOLD, never what they consented
// to.
func warnPromptFault(cmd *cobra.Command, err error) {
	if errors.Is(err, io.EOF) {
		return
	}
	clidiag.Fwarn(cmd.ErrOrStderr(), "ctxloom", "could not read your answer (%v); nothing was trusted or rejected", err)
}

// printBundleItemTrust stamps one bundle item by its ref and writes its trust
// line to w. The ref uses the local bundle name as its source, matching how the
// `fragment/prompt list` stamp addresses items of a materialized bundle.
func printBundleItemTrust(w io.Writer, stamper *operations.TrustStamper, bundle string, kind trust.ItemKind, name string) {
	ref := bundle + "#" + kind.Dir() + "/" + name
	res := stamper.ForRef(ref)
	// name is bundle-authored and reaches this print RAW; ForRef normalizes
	// its own copy for the trust decision (via trust.Ref.Key), but never
	// hands the cleaned string back. Strip (not NormalizeRef) so a malicious
	// name cannot repaint this terminal line without a second, redundant
	// warning on top of the one ForRef's ingest already emitted for the
	// same bytes.
	cleanName, _ := remote.StripRefControlChars(name)
	fmt.Fprintf(w, "  %s/%s: %s\n", kind.Dir(), cleanName, stampedTrust(res))
}

// printBundleHookTrust stamps one bundle hook by its (bundle, entry) identity and
// writes its trust line to w, mirroring printBundleItemTrust. Bundle hooks have
// no author-given name, so the line is keyed by the hook's "<event>/<index>" id —
// the same identity the exec choke and `ctxloom trust <bundle>#hooks/<id>`
// address — and the posture comes from the hook-aware TrustStamper.ForHook.
func printBundleHookTrust(w io.Writer, stamper *operations.TrustStamper, bundle string, entry bundles.HookEntry) {
	res := stamper.ForHook(bundle, entry)
	// entry.ID() is bundle-authored ("<event>/<index>", but the event name
	// comes straight from the bundle's hooks config) — same rationale as
	// printBundleItemTrust above.
	cleanID, _ := remote.StripRefControlChars(entry.ID())
	fmt.Fprintf(w, "  hooks/%s: %s\n", cleanID, stampedTrust(res))
}
