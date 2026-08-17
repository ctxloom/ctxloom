package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
)

// `ctxloom review` — the single review porcelain of the signature-envelope
// model (S6). It walks everything pending, grouped by bundle, shows each
// item's content (full for NEW, a unified diff against the previously
// approved snapshot for an UPDATE, the executable surface for mcp/hooks),
// and records the human's trust/reject decision by COUNTERSIGNING the exact
// reviewed bytes with their own SSH key — the signature IS the approval
// record. Off a TTY (or with --list) it degrades to the pending table so
// scripts and agents can see what a human still owes a look.

var (
	reviewListFlag    bool
	reviewProjectFlag bool
)

var reviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Review pending items: trust or reject what the agent may see",
	Long: `Walk every item awaiting review — remote content the agent has not been
allowed to see yet — grouped by bundle, and decide each one.

An item is pending when it was never reviewed, or when its content changed
since a human approved it (an UPDATE — shown as a diff against what was
approved). First-party content is exempt and never appears here: items you
authored in this project, builtin bundles shipped inside the binary, and
bundles signed by a publisher key you trust (allowed_signers). Trust is keyed
to a signing KEY, never to the remote the bytes arrived from. A rejection
still beats every one of those exemptions.

Per item: [t]rust, [r]eject, [s]kip, or [q]uit; [T] and [R] apply that answer
to the rest of the bundle. The letters are the CLI's own verbs, so what you
learn here works on the command line too.

Trusting COUNTERSIGNS the item's current content bytes with your SSH key (a
later change stops the signature verifying, re-gating it); rejecting
countersigns a refusal that is sticky — it survives the content changing, and
it beats a trusted publisher and project-local content alike. To undo either
decision, clear it with 'ctxloom bundle forget <ref>'; do not reach for a
rejection to withdraw an approval.

--project writes to the COMMITTABLE project store (.ctxloom/approvals) instead
of your personal one (~/.ctxloom/approvals), so a team/CI can inherit the
decision via the project's allowed_signers. It REQUIRES a signing key.

Non-interactive (piped, --list, or any --format but text): print the pending
table and exit.

The scriptable plumbing under this porcelain:
  ctxloom bundle trust <ref>   trust one item
  ctxloom bundle reject <ref>  reject one item
  ctxloom bundle forget <ref>  clear either decision, back to pending`,
	Args: cobra.NoArgs,
	RunE: runReviewCmd,
}

func runReviewCmd(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	return runReview(cmd, cfg)
}

func init() {
	reviewCmd.Flags().BoolVar(&reviewListFlag, "list", false, "List pending items without reviewing (non-interactive)")
	reviewCmd.Flags().BoolVar(&reviewProjectFlag, "project", false, "Write to the committable project store (.ctxloom/approvals); requires a signing key")
	rootCmd.AddCommand(reviewCmd)
}

// runReview enumerates the pending set and dispatches: the interactive walk on
// a TTY with no machine-readable --format asked for, the pending table
// otherwise (see reviewWantsListing).
func runReview(cmd *cobra.Command, cfg *config.Config) error {
	res, err := operations.PendingReview(cfg, operations.PendingReviewRequest{})
	if err != nil {
		return err
	}
	if reviewWantsListing(cmd, reviewListFlag, isInteractiveTerminal()) {
		return emit(cmd, res, func() error {
			renderReviewList(cmd.OutOrStdout(), res)
			return nil
		})
	}
	out := cmd.OutOrStdout()
	if res.Total == 0 {
		fmt.Fprintln(out, "Nothing is pending review.")
		return nil
	}

	// Resolve the countersigning key ONCE, up front — before the human has
	// spent any attention reading items (spec §9.5: "A review session that
	// cannot record its result is a waste of a human's attention and an
	// insult besides"). --project hard-requires a key; the personal store
	// degrades to the unsigned path with an explicit confirmation.
	signer, unsigned, err := resolveReviewSigner(cmd.Context(), agentkey.NewDiscoverer(), cfg.SignKey(), reviewProjectFlag)
	if err != nil {
		return err
	}
	if unsigned && !confirmUnsignedReview(out) {
		fmt.Fprintln(out, "Run 'ctxloom review' again once a signing key is available (see 'ssh-add').")
		return nil
	}
	if !unsigned {
		if !warnIfSoftwareKey(out, signer) {
			fmt.Fprintln(out, "Review cancelled.")
			return nil
		}
	}

	sum := runReviewWalk(out, promptLine, res, reviewApplier(cfg, reviewProjectFlag, signer))
	printReviewSummary(out, sum)
	if sum.accepted+sum.rejected > 0 {
		// One refresh for the whole session (not per item): reflect the new
		// decisions in the managed artifacts immediately, exactly like the
		// trust/reject plumbing does.
		refreshManagedArtifacts(cmd.Context(), cfg)
	}
	return nil
}

// resolveReviewSigner resolves the key `ctxloom review` will countersign
// with, via the unified zero-config discovery chain (internal/signing/agentkey:
// explicit key, then `git config user.signingkey`, then the sole ssh-agent
// identity — spec §7A.4).
//
// explicitKey is the caller's merged --key/sign.key value, exactly as
// `ctxloom sign` supplies it. Review used to pass "" here, so sign.key
// disambiguated signing but NOT approving: with several ssh-agent identities
// and no git user.signingkey, sign worked, `ctxloom doctor`'s SIGNKEY-k1
// check reported ok — and review still failed ambiguous (trim-gloss). "Unified
// chain" means the same inputs, not just the same function.
//
// project=true hard-errors when no key is available —
// spec §9.5: "ctxloom review --project therefore requires a key and refuses to
// run without one" — because an unsigned record in the COMMITTABLE store would
// be a forgery primitive with a friendly name. Otherwise a missing key
// degrades to (nil, true, nil): the caller offers the unsigned path.
func resolveReviewSigner(ctx context.Context, discoverer *agentkey.Discoverer, explicitKey string, project bool) (signer ssh.Signer, unsigned bool, err error) {
	discovered, agentErr := discoverer.Discover(ctx, explicitKey)
	if agentErr == nil {
		return discovered.Signer, false, nil
	}
	if project {
		return nil, false, fmt.Errorf(
			"no signing key available (%w) — 'ctxloom review --project' requires one; "+
				"run 'ssh-add ~/.ssh/id_ed25519' and try again, or review without --project", agentErr)
	}
	var ambiguous *agentkey.AmbiguousKeyError
	if errors.As(agentErr, &ambiguous) {
		// The event and the candidate listing belong to agentkey, which owns
		// both the error and what identifies a candidate. Re-authoring the
		// loop here is how this surface came to print fingerprint and key type
		// only, leaving a reviewer to choose between two ed25519 keys with
		// nothing to tell them apart. Only the closing line is review's own:
		// it is the surface that offers the unsigned path.
		fmt.Fprintln(os.Stderr, agentkey.AmbiguousKeyHeader)
		for _, line := range ambiguous.CandidateLines() {
			fmt.Fprintln(os.Stderr, line)
		}
		fmt.Fprintln(os.Stderr, "Pick one and re-run with SSH_AUTH_SOCK pointed at a single-identity agent, or continue unsigned below.")
	}
	// A *agentkey.NoKeyError (or any other discovery failure) also lands
	// here: no key anywhere in the chain degrades to the unsigned path,
	// same as the ambiguous case — only --project treats "cannot resolve a
	// key" as fatal.
	return nil, true, nil
}

// confirmUnsignedReview is the spec §9.5 degraded-path confirmation: it names
// the consequence — every process able to write .ctxloom/ (including an agent
// ctxloom launches) could forge such a record — and requires an explicit yes.
func confirmUnsignedReview(out io.Writer) bool {
	fmt.Fprintln(out, "No signing key found (checked git config user.signingkey, then ssh-agent via SSH_AUTH_SOCK).")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  ssh-add ~/.ssh/id_ed25519      load a key you already have, then re-run")
	fmt.Fprintln(out, "  ssh-keygen -t ed25519-sk       or generate a hardware key (recommended)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Or proceed WITHOUT a key: decisions are recorded UNSIGNED, locally only —")
	fmt.Fprintln(out, "exactly as safe as ctxloom before signing existed. Any process that can write")
	fmt.Fprintln(out, ".ctxloom/ (including an agent ctxloom launches) could forge such a record, and")
	fmt.Fprintln(out, "it is never shared or committed.")
	answer, err := promptLine("\nProceed unsigned? [y/N] ")
	if err != nil {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "y" || a == "yes"
}

// warnIfSoftwareKey is the spec §9.1.2 one-time-per-session posture warning:
// it fires when the key about to countersign with is NOT self-identifying as
// hardware-backed. Confirm-before-use (`ssh-add -c`) has no detectable
// signal (spec §9.1.2 — "I looked for another honest signal and there is
// none"), so only the hardware case is auto-detected; this always warns for
// a plain software key rather than guess at confirm-guarding.
//
// It is a WARNING, never a block — returns false only when the human
// explicitly chooses to quit; anything else proceeds. There is no persisted
// "don't ask again" yet (see the S6 report's deferred-work list): it fires
// once per `ctxloom review` invocation, not once ever.
func warnIfSoftwareKey(out io.Writer, signer ssh.Signer) bool {
	if signer == nil || agentkey.IsHardwareBacked(signer.PublicKey()) {
		return true
	}
	fmt.Fprintln(out, "Your approval key is a software key held in ssh-agent.")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s   %s\n", ssh.FingerprintSHA256(signer.PublicKey()), signer.PublicKey().Type())
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Any process holding SSH_AUTH_SOCK — including the coding agents ctxloom")
	fmt.Fprintln(out, "launches — can ask your agent to sign with this key. It cannot read the key,")
	fmt.Fprintln(out, "and it does not need to: it can simply request a signature. That means an")
	fmt.Fprintln(out, "agent can approve content for itself, which is the thing approvals exist to")
	fmt.Fprintln(out, "prevent.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Two fixes, either one closes it:")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  ssh-add -c ~/.ssh/id_ed25519      confirm each use — you get a prompt, the")
	fmt.Fprintln(out, "                                    agent gets nothing without your click")
	fmt.Fprintln(out, "  ssh-keygen -t ed25519-sk          hardware key — signing needs a physical touch")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Or run your agents in containers, where SSH_AUTH_SOCK is never forwarded.")
	answer, err := promptLine("\n[p] Proceed anyway / [q] Quit and fix this first: ")
	if err != nil {
		return true // EOF/read error: never block on a warning
	}
	return strings.ToLower(strings.TrimSpace(answer)) != "q"
}

// renderReviewList prints the non-interactive pending table: bundle, who signed
// it, then ref, kind, new|update — the refs are directly usable with
// trust/blacklist.
func renderReviewList(w io.Writer, res *operations.PendingReviewResult) {
	if res.Total == 0 {
		fmt.Fprintln(w, "Nothing is pending review.")
		return
	}
	fmt.Fprintf(w, "%d item(s) pending review (%d update(s)):\n", res.Total, res.Updates)
	for _, b := range res.Bundles {
		fmt.Fprintf(w, "\n%s", b.Ref)
		if b.Remote != "" {
			fmt.Fprintf(w, " (remote: %s)", b.Remote)
		}
		fmt.Fprintln(w)
		renderReviewPublisher(w, b)
		for _, it := range b.Items {
			fmt.Fprintf(w, "  %-8s %s/%s\n", it.Status, it.Kind, it.Name)
		}
	}
	fmt.Fprintln(w, "\nRun 'ctxloom review' in a terminal to review interactively, or use the")
	fmt.Fprintln(w, "plumbing per item: ctxloom bundle trust <bundle-ref>#<kind>/<name>, ctxloom bundle reject <ref>,")
	fmt.Fprintln(w, "or ctxloom bundle forget <ref> to clear a decision already made.")
}

// renderReviewPublisher prints the two lines that say WHO signed a pending
// bundle and what to do about it. Every item below is withheld either way, so
// what separates the three states is the NEXT COMMAND: an unsigned or
// trusted-signer bundle wants the human to read the content (`ctxloom review`),
// while a bundle signed by a key this machine does not trust has a second,
// entirely different fix available (`signer trust`) that the pending
// list is the only place to learn about.
//
// The untrusted wording carries the weight. A fingerprint printed in a surface
// like this one must not read as an ENDORSEMENT or as an identity: it is a
// string to compare against what the publisher told you out of band, exactly as
// SSH prints one for an unknown host. So the line leads with "untrusted key",
// never pairs the fingerprint with a name, and states the comparison as the
// step BEFORE the trust command rather than after it. Rendering it as
// "SHA256:… (Alice)" would be the failure: naming a principal nothing here has
// verified is the claim the trust root exists to make and this surface cannot.
func renderReviewPublisher(w io.Writer, b operations.ReviewBundle) {
	switch b.Publisher {
	case bundles.ReasonUntrustedSigner:
		fmt.Fprintf(w, "  signer:  untrusted key %s\n", b.SignerFingerprint)
		fmt.Fprintln(w, "           Signed, but by a key this machine does not trust to publish.")
		fmt.Fprintln(w, "           That fingerprint is a string to COMPARE, not a name: confirm it")
		fmt.Fprintln(w, "           with the publisher out of band, then trust the key by principal:")
		fmt.Fprintln(w, "             ctxloom signer trust <principal> --key <key.pub>")
	case bundles.ReasonTrustedSigner:
		fmt.Fprintf(w, "  signer:  %s — a key you trust to publish\n", b.Signer)
		fmt.Fprintln(w, "           Read the items and decide: ctxloom review")
	case bundles.ReasonUnsigned:
		fmt.Fprintln(w, "  signer:  none — these bytes carry no publisher signature")
		fmt.Fprintln(w, "           Nothing to compare; read the items and decide: ctxloom review")
	default:
		// A state this build does not know how to describe. Say NOTHING rather
		// than pick a wording: the whole point of the block above is that the
		// three states must not be rendered as each other, and a default that
		// guesses "unsigned" would reintroduce exactly the conflation being
		// removed — silently, for whatever state came later.
	}
}

// reviewDecision is the parsed per-item answer.
type reviewDecision int

const (
	reviewSkip reviewDecision = iota
	reviewTrust
	reviewReject
	reviewTrustBundle
	reviewRejectBundle
	reviewQuit
)

// parseReviewChoice maps a raw answer to a decision.
//
// The letters ARE the CLI's verbs — [t]rust and [r]eject spell what `ctxloom
// bundle trust` and `ctxloom bundle reject` write — so a reviewer learns one
// vocabulary here and can use it on the command line. The porcelain used to
// say "accept", which is a third word for the thing the plumbing and the store
// both call an approval.
//
// Each bulk form is its verb's UPPERCASE and nothing else. They are the widest
// actions offered, so neither may be reached by case-sloppy typing of the
// single form — and that rule matters more for [R] than for [T]: a bulk trust
// re-gates itself the moment any of those bytes change, while every rejection
// it writes is sticky.
//
// Everything else is a skip: the empty line, an unrecognized word, and the
// retired `a`/`A` accept spellings, which land on the safe side rather than
// silently approving on muscle memory. Viewing must never mutate trust.
func parseReviewChoice(answer string) reviewDecision {
	switch trimmed := strings.TrimSpace(answer); trimmed {
	case "t":
		return reviewTrust
	case "T":
		return reviewTrustBundle
	case "r":
		return reviewReject
	case "R":
		return reviewRejectBundle
	case "q", "Q":
		return reviewQuit
	default:
		return reviewSkip
	}
}

// reviewApplyFuncs are the mutation hooks the walk drives — injected so the
// walk is unit-testable without resolving real bundles; production wires the
// operations plumbing (reviewApplier).
type reviewApplyFuncs struct {
	accept func(ref string) error
	reject func(ref string) error
}

// reviewApplier routes accept/reject through the SAME operations the
// standalone trust/blacklist commands use, so the porcelain and the plumbing
// write identical countersignatures. signer/project are resolved once for the
// whole session (see runReview) and threaded through every mutation so a
// session countersigns consistently with one key, to one store.
func reviewApplier(cfg *config.Config, project bool, signer ssh.Signer) reviewApplyFuncs {
	return reviewApplyFuncs{
		accept: func(ref string) error {
			_, err := operations.SetItemTrust(cfg, operations.SetItemTrustRequest{Ref: ref, Project: project, Signer: signer})
			return err
		},
		reject: func(ref string) error {
			res, err := operations.SetBlacklist(cfg, operations.SetBlacklistRequest{Ref: ref, Project: project, Signer: signer})
			if err == nil && len(res.ContentForms) == 0 {
				clidiag.Warn("ctxloom", "could not countersign %q's content; the ref-level rejection applies, but no content-reject countersignature was recorded", ref)
			}
			return err
		},
	}
}

// reviewSummary tallies one review session.
type reviewSummary struct {
	total    int
	accepted int
	rejected int
	skipped  int
}

func (s reviewSummary) stillPending() int { return s.total - s.accepted - s.rejected }

// runReviewWalk is the interactive core: per bundle print a header, per item
// show the content (or update diff) and apply the prompted decision. A read
// error (EOF, closed stdin) quits the walk — reviewing must never mutate
// without an explicit answer. Apply failures warn and count the item as
// skipped (fault tolerance: one unresolvable item never aborts the session).
// A bulk answer applies to the REST OF ONE BUNDLE and is reset at the next
// bundle header, so a reviewer can never decide, in one keystroke, about
// content they were never shown.
func runReviewWalk(out io.Writer, prompt func(string) (string, error), res *operations.PendingReviewResult, apply reviewApplyFuncs) reviewSummary {
	sum := reviewSummary{total: res.Total}
	trust := func(ref string) { applyReviewDecision(out, apply.accept, ref, "trusted", &sum.accepted, &sum.skipped) }
	reject := func(ref string) { applyReviewDecision(out, apply.reject, ref, "rejected", &sum.rejected, &sum.skipped) }
	for _, b := range res.Bundles {
		printReviewBundleHeader(out, b)
		var rest func(string)
		for i, item := range b.Items {
			if rest != nil {
				rest(item.Ref)
				continue
			}
			printReviewItem(out, i+1, len(b.Items), item)
			answer, err := prompt("[t]rust / [r]eject / [s]kip / [T] trust or [R] reject rest of bundle / [q]uit: ")
			if err != nil {
				return sum // EOF/read error → quit; no answer, no mutation
			}
			switch parseReviewChoice(answer) {
			case reviewTrust:
				trust(item.Ref)
			case reviewReject:
				reject(item.Ref)
			case reviewTrustBundle:
				rest = trust
				trust(item.Ref)
			case reviewRejectBundle:
				rest = reject
				reject(item.Ref)
			case reviewQuit:
				return sum
			default:
				sum.skipped++
			}
		}
	}
	return sum
}

// applyReviewDecision runs one mutation, echoes the outcome, and tallies it. A
// failure warns and counts the item as skipped — it stays pending for a later
// session rather than sinking this one.
func applyReviewDecision(out io.Writer, apply func(string) error, ref, verb string, tally, skipped *int) {
	if err := apply(ref); err != nil {
		clidiag.Warn("ctxloom", "could not record decision for %q: %v", ref, err)
		*skipped++
		return
	}
	fmt.Fprintf(out, "  → %s %s\n", verb, ref)
	*tally++
}

// printReviewBundleHeader names the bundle, its source remote, and the counts.
func printReviewBundleHeader(w io.Writer, b operations.ReviewBundle) {
	updates := 0
	for _, it := range b.Items {
		if it.Status == operations.ReviewStatusUpdate {
			updates++
		}
	}
	fmt.Fprintf(w, "\n━━ %s", b.Ref)
	if b.Remote != "" {
		fmt.Fprintf(w, " (remote: %s)", b.Remote)
	}
	fmt.Fprintf(w, " — %d pending", len(b.Items))
	if updates > 0 {
		fmt.Fprintf(w, " (%d update(s))", updates)
	}
	fmt.Fprintln(w)
}

// printReviewItem shows one item: full content for NEW items and executables
// (mcp/hooks always render as what they run), a unified diff against the
// previously-accepted snapshot for an UPDATE — falling back to full content
// when no snapshot exists (e.g. a migrated v1 grant).
func printReviewItem(w io.Writer, idx, count int, item operations.ReviewItem) {
	label := "NEW"
	if item.Status == operations.ReviewStatusUpdate {
		label = "UPDATE — changed since acceptance"
	}
	fmt.Fprintf(w, "\n[%d/%d] %s/%s (%s)\n", idx, count, item.Kind, item.Name, label)
	if item.AlternateContent != "" {
		// Both forms follow, so the exposed one must be named too — an
		// unlabelled block above a labelled one reads as "the only form".
		fmt.Fprintf(w, "  --- %s form (exposed now) ---\n", item.CurrentForm)
	}

	printReviewItemBody(w, item)
	printReviewAlternateForm(w, item)
}

// printReviewItemBody renders the bytes under an item's header: a unified diff
// against the previously-accepted snapshot when one exists and the content
// moved, the full content otherwise. Every fall-through to full content names
// its reason — an unexplained wall of content is indistinguishable from a
// change the reviewer failed to spot.
func printReviewItemBody(w io.Writer, item operations.ReviewItem) {
	isUpdate := item.Status == operations.ReviewStatusUpdate
	switch {
	case isUpdate && item.PreviousContent != "":
		switch diff := unifiedReviewDiff(item.PreviousContent, item.CurrentContent); {
		case diff != "":
			fmt.Fprint(w, indentBlock(diff))
			return
		case item.PreviousContent == item.CurrentContent:
			// An item is labelled UPDATE whenever a prior approval exists, not
			// only when the bytes moved — a superseded approval record (e.g. a
			// countersign-contract bump) re-gates identical content. Full
			// content below is then the whole story, and saying so is the
			// difference between a re-read and a re-audit.
			fmt.Fprintln(w, "  (unchanged since it was approved — it is pending again because the earlier approval no longer applies; showing it in full)")
		default:
			fmt.Fprintln(w, "  (no differences could be rendered against the approved content — showing the incoming content in full)")
		}
	case isUpdate && !item.Executable:
		// No snapshot to diff against (e.g. a migrated grant). Executables are
		// exempt: mcp/hooks always render as what they run, never as a diff.
		fmt.Fprintln(w, "  (no snapshot of the previously accepted content — showing it in full)")
	}
	fmt.Fprint(w, indentBlock(item.CurrentContent))
}

// printReviewAlternateForm shows the item's OTHER form when it has one.
// Accepting countersigns both the raw and the distilled bytes, so a reviewer
// shown only the currently-exposed form would bless content they never read —
// and flipping use_distilled would then serve it without re-gating.
// The header names both forms so it is unambiguous which bytes
// the decision covers.
func printReviewAlternateForm(w io.Writer, item operations.ReviewItem) {
	if item.AlternateContent == "" {
		return
	}
	fmt.Fprintf(w, "\n  --- %s form (also covered by this approval) ---\n", item.AlternateForm)
	fmt.Fprint(w, indentBlock(item.AlternateContent))
}

// unifiedReviewDiff renders a unified diff of the accepted vs incoming
// content. Returns "" for an empty delta, on which the caller falls back to
// the full-content display and says why.
//
// The error is discarded because it cannot occur: GetUnifiedDiffString renders
// into a bytes.Buffer of its own, and bytes.Buffer writes never return an
// error (an over-large buffer panics instead). There is no failure mode here
// to distinguish from an empty delta.
func unifiedReviewDiff(previous, current string) string {
	text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(previous),
		B:        difflib.SplitLines(current),
		FromFile: "accepted",
		ToFile:   "incoming",
		Context:  3,
	})
	if err != nil {
		return ""
	}
	return text
}

// indentBlock indents every line of text by two spaces (trailing newline
// guaranteed) so item content reads as a block under its header.
func indentBlock(text string) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return "  (empty)\n"
	}
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// printReviewSummary reports the session tally.
func printReviewSummary(w io.Writer, sum reviewSummary) {
	fmt.Fprintf(w, "\nReview complete: %d trusted, %d rejected, %d skipped — %d still pending.\n",
		sum.accepted, sum.rejected, sum.skipped, sum.stillPending())
}
