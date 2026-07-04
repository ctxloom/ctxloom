package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// `ctxloom review` — the single review porcelain of the three-state model
// (trust-simplify slice 2). It walks everything pending, grouped by bundle,
// shows each item's content (full for NEW, a unified diff against the
// previously-accepted snapshot for an UPDATE, the executable surface for
// mcp/hooks), and records the human's accept/reject decision through the same
// operations the `ctxloom trust`/`blacklist` plumbing uses — one mutation path,
// identical on-disk results. Off a TTY (or with --list) it degrades to the
// pending table so scripts and agents can see what a human still owes a look.

var reviewListFlag bool

var reviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Review pending items: accept or reject what the agent may see",
	Long: `Walk every item awaiting review — remote content the agent has not been
allowed to see yet — grouped by bundle, and decide each one.

An item is pending when it was never reviewed, or when its content changed
since a human accepted it (an UPDATE — shown as a diff against what was
accepted). First-party content (project-local, trusted sources) is exempt and
never appears here.

Per item: [a]ccept, [r]eject, [s]kip, [A] accept all remaining in the bundle,
or [q]uit. Accepting binds the item to its current content-hash pair (a later
change re-gates it); rejecting withholds it permanently, content hash included.

Non-interactive (piped, or --list): print the pending table and exit.

The scriptable plumbing under this porcelain:
  ctxloom trust <ref>       accept one item
  ctxloom blacklist <ref>   reject one item`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		return runReview(cmd, cfg)
	},
}

func init() {
	reviewCmd.Flags().BoolVar(&reviewListFlag, "list", false, "List pending items without reviewing (non-interactive)")
	rootCmd.AddCommand(reviewCmd)
}

// runReview enumerates the pending set and dispatches: the interactive walk on
// a TTY, the pending table otherwise.
func runReview(cmd *cobra.Command, cfg *config.Config) error {
	res, err := operations.PendingReview(cfg, operations.PendingReviewRequest{})
	if err != nil {
		return err
	}
	if reviewListFlag || !isInteractiveTerminal() {
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
	sum := runReviewWalk(out, promptLine, res, reviewApplier(cfg))
	printReviewSummary(out, sum)
	if sum.accepted+sum.rejected > 0 {
		// One refresh for the whole session (not per item): reflect the new
		// decisions in the managed artifacts immediately, exactly like the
		// trust/blacklist plumbing does.
		refreshManagedArtifacts(cmd.Context(), cfg)
	}
	return nil
}

// renderReviewList prints the non-interactive pending table: bundle, ref,
// kind, new|update — the refs are directly usable with trust/blacklist.
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
		for _, it := range b.Items {
			fmt.Fprintf(w, "  %-8s %s/%s\n", it.Status, it.Kind, it.Name)
		}
	}
	fmt.Fprintln(w, "\nRun 'ctxloom review' in a terminal to review interactively, or use the")
	fmt.Fprintln(w, "plumbing per item: ctxloom trust <bundle-ref>#<kind>/<name> / ctxloom blacklist <ref>.")
}

// reviewDecision is the parsed per-item answer.
type reviewDecision int

const (
	reviewSkip reviewDecision = iota
	reviewAccept
	reviewReject
	reviewAcceptBundle
	reviewQuit
)

// parseReviewChoice maps a raw answer to a decision. Only explicit letters
// act; anything else — including the empty line and an unrecognized word — is
// a skip, because viewing must never mutate trust. The accept-all shortcut is
// the UPPERCASE 'A' only: it is the widest action offered, so it must not be
// reachable by case-sloppy typing of the single accept.
func parseReviewChoice(answer string) reviewDecision {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "A" {
		return reviewAcceptBundle
	}
	switch strings.ToLower(trimmed) {
	case "a":
		return reviewAccept
	case "r":
		return reviewReject
	case "q":
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
// write identical states (hash pair + snapshots on accept; ref block +
// content-hash denylist on reject).
func reviewApplier(cfg *config.Config) reviewApplyFuncs {
	return reviewApplyFuncs{
		accept: func(ref string) error {
			_, err := operations.SetItemTrust(cfg, operations.SetItemTrustRequest{Ref: ref})
			return err
		},
		reject: func(ref string) error {
			res, err := operations.SetBlacklist(cfg, operations.SetBlacklistRequest{Ref: ref})
			if err == nil && len(res.ContentHashes) == 0 {
				clidiag.Warn("ctxloom", "could not hash %q; the ref-level rejection applies, but no content-denylist entry was recorded", ref)
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
func runReviewWalk(out io.Writer, prompt func(string) (string, error), res *operations.PendingReviewResult, apply reviewApplyFuncs) reviewSummary {
	sum := reviewSummary{total: res.Total}
	for _, b := range res.Bundles {
		printReviewBundleHeader(out, b)
		acceptRest := false
		for i, item := range b.Items {
			if acceptRest {
				applyReviewDecision(out, apply.accept, item.Ref, "accepted", &sum.accepted, &sum.skipped)
				continue
			}
			printReviewItem(out, i+1, len(b.Items), item)
			answer, err := prompt("[a]ccept / [r]eject / [s]kip / [A] accept rest of bundle / [q]uit: ")
			if err != nil {
				return sum // EOF/read error → quit; no answer, no mutation
			}
			switch parseReviewChoice(answer) {
			case reviewAccept:
				applyReviewDecision(out, apply.accept, item.Ref, "accepted", &sum.accepted, &sum.skipped)
			case reviewReject:
				applyReviewDecision(out, apply.reject, item.Ref, "rejected", &sum.rejected, &sum.skipped)
			case reviewAcceptBundle:
				acceptRest = true
				applyReviewDecision(out, apply.accept, item.Ref, "accepted", &sum.accepted, &sum.skipped)
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

	if item.Status == operations.ReviewStatusUpdate && item.PreviousContent != "" {
		if diff := unifiedReviewDiff(item.PreviousContent, item.CurrentContent); diff != "" {
			fmt.Fprint(w, indentBlock(diff))
			return
		}
	}
	if item.Status == operations.ReviewStatusUpdate && item.PreviousContent == "" && !item.Executable {
		fmt.Fprintln(w, "  (no snapshot of the previously accepted content — showing it in full)")
	}
	fmt.Fprint(w, indentBlock(item.CurrentContent))
}

// unifiedReviewDiff renders a unified diff of the accepted vs incoming
// content. Returns "" on failure or an empty delta so the caller falls back
// to the full-content display.
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
	fmt.Fprintf(w, "\nReview complete: %d accepted, %d rejected, %d skipped — %d still pending.\n",
		sum.accepted, sum.rejected, sum.skipped, sum.stillPending())
}

// --- init wiring -----------------------------------------------------------------

// offerInitReview closes init's interactive path (trust-simplify slice 2):
// after remotes are configured and the first pull/sync has run — and after the
// discovery session, whose installs can add pending items — it offers one
// inline review session when anything is pending. Non-interactive init prints
// the count and the command instead. Fault-tolerant throughout: no failure in
// here may abort init (CLAUDE.md), so every error degrades to a warning.
//
// Interactive reads go through a shared-stdin-handoff lease (not the global
// promptLine reader) so a byte the discovery session's stdin pump had in
// flight when its run ended is delivered to the first prompt here instead of
// being lost — the same pattern as offerSessionRelaunch.
func offerInitReview(cmd *cobra.Command, interactive bool) {
	cfg, err := config.Load()
	if err != nil {
		clidiag.Warn("ctxloom", "skipping review offer (config unreadable): %v", err)
		return
	}
	res, err := operations.PendingReview(cfg, operations.PendingReviewRequest{})
	if err != nil {
		clidiag.Warn("ctxloom", "could not enumerate items pending review: %v", err)
		return
	}
	if res.Total == 0 {
		return
	}
	if !interactive {
		fmt.Printf("%d item(s) await review — run 'ctxloom review' to accept or reject them.\n", res.Total)
		return
	}

	lease := sharedStdinHandoff().Attach()
	defer lease.Detach()
	reader := bufio.NewReader(lease)
	prompt := func(p string) (string, error) {
		fmt.Fprint(os.Stderr, p)
		line, rerr := reader.ReadString('\n')
		if rerr != nil {
			return "", rerr
		}
		return strings.TrimSpace(line), nil
	}

	answer, err := prompt(fmt.Sprintf("\n%d item(s) await review. Review now? [Y/n] ", res.Total))
	if err != nil || !wantsInitReview(answer) {
		fmt.Println("Run 'ctxloom review' any time to review them.")
		return
	}
	sum := runReviewWalk(os.Stdout, prompt, res, reviewApplier(cfg))
	printReviewSummary(os.Stdout, sum)
	if sum.accepted+sum.rejected > 0 {
		refreshManagedArtifacts(cmd.Context(), cfg)
	}
}

// wantsInitReview interprets the Y/n answer: empty means yes (the default);
// only an explicit n/no declines — mirroring wantsRelaunch.
func wantsInitReview(answer string) bool {
	a := strings.ToLower(strings.TrimSpace(answer))
	return a != "n" && a != "no"
}
