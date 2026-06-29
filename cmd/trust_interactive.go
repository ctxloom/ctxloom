package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Interactive trust review/marking surface (trust rework, TR4). The `show -i`
// flags render an item's (or bundle's) effective trust + source through the TR3
// resolver and then OFFER an explicit mutation — viewing never trusts. Every
// surface is TTY-gated by the caller (isInteractiveTerminal): with a piped/
// redirected stdout the content path is untouched and no trust UI is emitted, so
// `show` output stays byte-for-byte identical for scripts. All trust UI is
// written to stderr so it never mingles with the content on stdout.
//
// The actual mutations reuse the single TR2 path (runItemTrust / runBlacklist /
// runBundleTrust → operations.Set*), and the interactive read reuses the single
// shared stdin reader via promptLine / promptYesNo (run.go) — no second
// bufio.Reader is created (ctxloom-code-08-002).

// itemTrustChoice is the parsed decision from the `[t]rust / [b]lacklist / skip`
// menu. It is split out of the raw terminal read so the action it drives is unit
// testable without a TTY (see TestApplyItemTrustChoice_*).
type itemTrustChoice int

const (
	itemTrustSkip itemTrustChoice = iota
	itemTrustGrant
	itemTrustBlacklist
)

// parseItemTrustChoice maps a raw interactive answer to an action. Only an
// explicit "t"/"b" trusts or blacklists; anything else — including the empty
// line, "s", or an EOF-truncated read — is a skip, because viewing must never
// mutate trust.
func parseItemTrustChoice(answer string) itemTrustChoice {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "t":
		return itemTrustGrant
	case "b":
		return itemTrustBlacklist
	default:
		return itemTrustSkip
	}
}

// applyItemTrustChoice performs the chosen mutation through the same TR2
// operations the standalone `ctxloom trust`/`blacklist` commands use, so there
// is exactly one mutation path and the on-disk result is identical. Skip is a
// no-op: viewing an item never trusts it. cfg- and cmd-injectable so a test can
// drive each branch against a temp project and assert the written store.
func applyItemTrustChoice(cmd *cobra.Command, cfg *config.Config, ref string, choice itemTrustChoice) error {
	switch choice {
	case itemTrustGrant:
		return runItemTrust(cmd, cfg, ref)
	case itemTrustBlacklist:
		return runBlacklist(cmd, cfg, ref)
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
// performs the explicit choice. EOF / read error is treated as skip.
func offerItemTrust(cmd *cobra.Command, cfg *config.Config, ref string) error {
	res := operations.NewTrustStamper(cfg).ForRef(ref)
	fmt.Fprintf(os.Stderr, "\nEffective trust: %s\n", stampedTrust(res))
	answer, err := promptLine("[t]rust / [b]lacklist / skip? ")
	if err != nil {
		return nil // EOF/read error → skip; viewing never trusts
	}
	return applyItemTrustChoice(cmd, cfg, ref, parseItemTrustChoice(answer))
}

// offerBundleTrust is the TTY-gated interactive trust review for `bundle show
// <name> -i`. It prints every item's effective trust + source — fragments,
// prompts, MCP servers, AND bundle hooks — resolved through one shared TR3
// TrustStamper (store + registry read once, each bundle materialized once) to
// stderr. It then offers an explicit per-hook [t]rust/[b]lacklist action (hooks
// are executables the SHA-agnostic posture cannot content-pin) and finally offers
// to mark the whole bundle trusted (a SHA-agnostic posture via SetBundleTrust).
// The bundle body is already on stdout; only an explicit action writes.
func offerBundleTrust(cmd *cobra.Command, cfg *config.Config, name string, bundle *bundles.Bundle) error {
	stamper := operations.NewTrustStamper(cfg)
	fmt.Fprintf(os.Stderr, "\nPer-item effective trust for bundle %q:\n", name)
	for _, n := range bundle.FragmentNames() {
		printBundleItemTrust(os.Stderr, stamper, name, trust.KindFragment, n)
	}
	for _, n := range bundle.PromptNames() {
		printBundleItemTrust(os.Stderr, stamper, name, trust.KindPrompt, n)
	}
	for _, n := range bundle.MCPNames() {
		printBundleItemTrust(os.Stderr, stamper, name, trust.KindMCP, n)
	}
	for _, e := range bundle.Hooks.Entries() {
		printBundleHookTrust(os.Stderr, stamper, name, e)
	}

	// Bundle hooks are arbitrary-command executables the cascade never
	// auto-trusts, and the SHA-agnostic whole-bundle posture below cannot pin a
	// hook's executable surface — so offer an explicit per-hook [t]rust/[b]lacklist
	// (a content-pinned grant / sticky block) before the bundle-grain prompt,
	// routed through the same TR2 path as `ctxloom trust|blacklist
	// <bundle>#hooks/<event>/<index>`. Viewing never trusts.
	if err := offerBundleHookTrust(cmd, cfg, name, bundle); err != nil {
		return err
	}

	yes, err := promptYesNo("\nMark bundle trusted? [y/N] ")
	if err != nil || !yes {
		return nil // viewing never trusts
	}
	return runBundleTrust(cmd, cfg, name, true)
}

// offerBundleHookTrust walks the bundle's hooks in canonical identity order and
// offers an explicit [t]rust/[b]lacklist/skip action per hook, applying the
// choice through the shared applyItemTrustChoice path so the on-disk result is
// identical to `ctxloom trust|blacklist <bundle>#hooks/<event>/<index>`. A read
// error (EOF) stops the walk and skips the rest — viewing never trusts. A
// hookless bundle is a no-op (no prompt emitted).
func offerBundleHookTrust(cmd *cobra.Command, cfg *config.Config, name string, bundle *bundles.Bundle) error {
	entries := bundle.Hooks.Entries()
	if len(entries) == 0 {
		return nil
	}
	fmt.Fprintln(os.Stderr, "\nBundle hooks are executable surfaces — trust or blacklist each:")
	for _, e := range entries {
		ref := name + "#hooks/" + e.ID()
		answer, err := promptLine(fmt.Sprintf("  hooks/%s — [t]rust / [b]lacklist / skip? ", e.ID()))
		if err != nil {
			return nil // EOF/read error → skip the remaining hooks; viewing never trusts
		}
		if err := applyItemTrustChoice(cmd, cfg, ref, parseItemTrustChoice(answer)); err != nil {
			return err
		}
	}
	return nil
}

// printBundleItemTrust stamps one bundle item by its ref and writes its trust
// line to w. The ref uses the local bundle name as its source, matching how the
// `fragment/prompt list` stamp addresses items of a materialized bundle.
func printBundleItemTrust(w io.Writer, stamper *operations.TrustStamper, bundle string, kind trust.ItemKind, name string) {
	ref := bundle + "#" + kind.Dir() + "/" + name
	res := stamper.ForRef(ref)
	fmt.Fprintf(w, "  %s/%s: %s\n", kind.Dir(), name, stampedTrust(res))
}

// printBundleHookTrust stamps one bundle hook by its (bundle, entry) identity and
// writes its trust line to w, mirroring printBundleItemTrust. Bundle hooks have
// no author-given name, so the line is keyed by the hook's "<event>/<index>" id —
// the same identity the exec choke and `ctxloom trust <bundle>#hooks/<id>`
// address — and the posture comes from the hook-aware TrustStamper.ForHook.
func printBundleHookTrust(w io.Writer, stamper *operations.TrustStamper, bundle string, entry bundles.HookEntry) {
	res := stamper.ForHook(bundle, entry)
	fmt.Fprintf(w, "  hooks/%s: %s\n", entry.ID(), stampedTrust(res))
}

// reviewLocalMCPTrust is the TTY-gated trust review for `manage mcp servers show
// -i`. The servers it lists are project/plugin-local configured executables, not
// bundle items: the trust model never auto-trusts them (TR3 ForLocalMCP) and —
// because they carry no bundle ref — they have no SetItemTrust/SetBlacklist
// mutation path. This surface therefore REVIEWS the posture (to stderr) and
// points at the ref-based commands for bundle-sourced MCP servers, rather than
// offering a t/b action that could not be honored. (See follow-up: a local-MCP
// grant primitive is out of TR4 scope.)
func reviewLocalMCPTrust(cfg *config.Config, entries []operations.MCPServerEntry) {
	stamper := operations.NewTrustStamper(cfg)
	for _, e := range entries {
		res := stamper.ForLocalMCP(e.Name, bundles.BundleMCP{
			Command:      e.Command,
			Args:         e.Args,
			Env:          e.Env,
			Installation: e.Installation,
		})
		fmt.Fprintf(os.Stderr, "Effective trust (%s scope): %s\n", e.Backend, stampedTrust(res))
	}
	fmt.Fprintln(os.Stderr,
		"Configured local MCP servers are reviewed here; grant or blacklist a bundle-sourced "+
			"MCP server with `ctxloom trust|blacklist <bundle>#mcp/<name>`.")
}
