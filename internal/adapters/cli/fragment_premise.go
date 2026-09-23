package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/termsafe"
)

// Premise authoring is a PROPOSAL surface: the model drafts and critiques, and
// only a human at a terminal ratifies. Off a terminal, or with --dry-run, the
// command presents and writes nothing. Accepting writes the premise and never
// signs — the premise is inside the item's trust preimage, so signing is the
// author's deliberate `bundle sign`, not a side effect of saying yes.

var (
	fragmentDraftPremiseLLM        string
	fragmentDraftPremiseNoCritique bool
	fragmentDraftPremiseDryRun     bool
	fragmentDraftPremisePromptDir  string
)

var fragmentDraftPremiseCmd = &cobra.Command{
	Use:   "draft-premise <bundle>#fragments/<name>",
	Short: "Draft a premise for a fragment, critique it, and accept, edit or reject it",
	Long: `Draft a premise for a fragment with an LLM, attack the draft with a second,
independent LLM pass, and put both in front of you to accept, edit or reject.

A premise states WHEN a fragment applies; a fragment with a premise is loaded
conditionally, and one without is always loaded. The draft proposes a premise
(or NONE for always-load), the moments it fires on, notes on why the fragment
exists, and — prominently — whether the fragment should be split instead.

Only a human at a terminal ratifies. When stdout is not a terminal, or with
--dry-run, the proposal is printed (use --format json for a structured one)
and nothing is written.

Accepting or editing writes the premise; it never signs. A changed premise
leaves the item's approvals stale until you run 'ctxloom bundle sign <bundle>'.

In the editor, always-load must be written as the literal NONE: an emptied
premise is refused rather than read as always-load.

Examples:
  ctxloom fragment draft-premise core#fragments/tdd
  ctxloom fragment draft-premise core#fragments/tdd --no-critique
  ctxloom fragment draft-premise core#fragments/tdd --dry-run --format json`,
	Args: cobra.ExactArgs(1),
	RunE: runFragmentDraftPremise,
}

func init() {
	fragmentCmd.AddCommand(fragmentDraftPremiseCmd)
	f := fragmentDraftPremiseCmd.Flags()
	f.StringVar(&fragmentDraftPremiseLLM, "llm", "", "LLM label to draft and critique with (default: llm.defaults.primary)")
	f.BoolVar(&fragmentDraftPremiseNoCritique, "no-critique", false, "Skip the adversarial critique pass")
	f.BoolVar(&fragmentDraftPremiseDryRun, "dry-run", false, "Present the proposal and write nothing, even at a terminal")
	f.StringVar(&fragmentDraftPremisePromptDir, "prompt-dir", "", "Load premise-author.md / premise-critique.md from this directory instead of the built-in prompts")
}

// newPremiseRunner starts a FRESH internal one-shot (at plan, per
// InternalSource) and returns its turn and its end. Each pass calls it
// separately: a critique sharing the drafter's session would be self-review.
// A var so a test can supply canned runners without an engine.
var newPremiseRunner = func(ctx context.Context, cfg *config.Config, label string) (memory.Runner, func(), error) {
	s, err := operations.StartInternalOneShot(ctx, App().Engines(), internalRunHosts(), cfg, App().Strictness, label, "", projectroot.WorkDir(), "", 0)
	if err != nil {
		return nil, nil, fmt.Errorf("no reachable engine for premise authoring (label %q): %w", label, err)
	}
	return s.Turn, s.End, nil
}

// premiseValues is a fragment's premise and notes, current or proposed.
type premiseValues struct {
	Premise string `json:"premise"`
	Notes   string `json:"notes,omitempty"`
}

// premiseProposal is what the command presents and emits. Decision is
// "proposed" when nothing could be ratified (no terminal, --dry-run), else the
// author's choice.
type premiseProposal struct {
	Ref      string                      `json:"ref"`
	Current  premiseValues               `json:"current"`
	Draft    *operations.PremiseDraft    `json:"draft"`
	Critique *operations.PremiseCritique `json:"critique,omitempty"`
	Decision string                      `json:"decision"`
	Written  *premiseValues              `json:"written,omitempty"`
	// StaleApprovals is set when the written premise differs from the current
	// one: the premise is signed, notes are not.
	StaleApprovals bool `json:"stale_approvals,omitempty"`
}

const (
	premiseDecisionProposed = "proposed"
	premiseDecisionAccepted = "accepted"
	premiseDecisionEdited   = "edited"
	premiseDecisionRejected = "rejected"
)

// errPremiseEditEmpty refuses an edited premise left blank: always-load must
// be said as NONE, so an accidentally emptied field cannot mean it silently.
var errPremiseEditEmpty = errors.New("edited premise is empty: write NONE to make the fragment always load")

func runFragmentDraftPremise(cmd *cobra.Command, args []string) error {
	ref := args[0]
	bundleName, itemName, err := itemRefTarget(ref, ItemTypeFragment)
	if err != nil {
		return err
	}
	ref = bundleName + "#" + itemRefPrefix(ItemTypeFragment) + itemName
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	cur, err := operations.GetItemContent(ctx, cfg, operations.GetItemRequest{Bundle: bundleName, Kind: ItemTypeFragment, Name: itemName})
	if err != nil {
		return err
	}
	label := fragmentDraftPremiseLLM
	if label == "" {
		label = cfg.PrimaryLabel()
	}
	if label == "" {
		return fmt.Errorf("no LLM label resolves for premise authoring: pass --llm, or set llm.defaults.primary in config.yaml")
	}

	p := &premiseProposal{Ref: ref, Current: premiseValues{Premise: cur.Premise, Notes: cur.Notes}, Decision: premiseDecisionProposed}
	if p.Draft, err = runPremisePass(ctx, cfg, label, func(c operations.PremiseAuthorConfig) (*operations.PremiseDraft, error) {
		return operations.DraftPremise(ctx, c, ref, cur.Content)
	}); err != nil {
		return err
	}
	if !fragmentDraftPremiseNoCritique {
		siblings, err := premiseSiblings(cfg, ref)
		if err != nil {
			return err
		}
		if p.Critique, err = runPremisePass(ctx, cfg, label, func(c operations.PremiseAuthorConfig) (*operations.PremiseCritique, error) {
			return operations.CritiquePremise(ctx, c, ref, cur.Content, p.Draft, siblings)
		}); err != nil {
			return err
		}
	}

	if fragmentDraftPremiseDryRun || !isInteractiveTerminal() {
		return emit(cmd, p, func() error {
			w := cmd.OutOrStdout()
			renderPremiseProposal(w, p)
			_, err := fmt.Fprintln(w, "\nNothing written: only a human at a terminal can accept a premise (and not with --dry-run).")
			return err
		})
	}

	renderPremiseProposal(cmd.OutOrStdout(), p)
	if err := decidePremise(cfg, p); err != nil {
		return err
	}
	if p.Written != nil {
		if _, err := operations.SetFragmentPremise(ctx, cfg, operations.SetFragmentPremiseRequest{
			Bundle: bundleName, Name: itemName, Premise: p.Written.Premise, Notes: p.Written.Notes,
		}); err != nil {
			return err
		}
		p.StaleApprovals = p.Written.Premise != cur.Premise
	}
	return emit(cmd, p, func() error {
		w := cmd.OutOrStdout()
		if p.Written == nil {
			_, err := fmt.Fprintln(w, "Rejected: nothing written.")
			return err
		}
		fmt.Fprintf(w, "Wrote the premise for %s.\n", termsafe.Field(ref))
		if p.StaleApprovals {
			fmt.Fprintf(w, "The item's approvals are now stale: run 'ctxloom bundle sign %s' to ratify it.\n", termsafe.Field(bundleName))
		}
		return nil
	})
}

// runPremisePass runs one pass on its own fresh session, ended as soon as the
// pass returns.
func runPremisePass[T any](ctx context.Context, cfg *config.Config, label string, pass func(operations.PremiseAuthorConfig) (T, error)) (T, error) {
	var zero T
	run, end, err := newPremiseRunner(ctx, cfg, label)
	if err != nil {
		return zero, err
	}
	defer end()
	return pass(operations.PremiseAuthorConfig{Run: run, PromptDir: fragmentDraftPremisePromptDir})
}

// premiseSiblings is the premise index minus the fragment being authored: the
// premises the draft could collide with.
func premiseSiblings(cfg *config.Config, ref string) ([]operations.PremiseIndexEntry, error) {
	entries, err := operations.PremiseIndex(cfg.BundleLoader().Catalog())
	if err != nil {
		return nil, fmt.Errorf("failed to list sibling premises: %w", err)
	}
	siblings := make([]operations.PremiseIndexEntry, 0, len(entries))
	for _, e := range entries {
		if e.Name != ref {
			siblings = append(siblings, e)
		}
	}
	return siblings, nil
}

// decidePremise asks the author to accept, edit or reject, setting Decision
// and, unless rejected, Written. An unreadable prompt is a reject: nothing is
// ever written without an explicit yes.
func decidePremise(cfg *config.Config, p *premiseProposal) error {
	for {
		answer, err := promptLine("[a]ccept / [e]dit / [r]eject? ")
		if err != nil {
			p.Decision = premiseDecisionRejected
			return nil
		}
		switch strings.ToLower(answer) {
		case "a", "accept":
			p.Decision = premiseDecisionAccepted
			p.Written = &premiseValues{Premise: p.Draft.Premise, Notes: proposedNotes(p)}
			return nil
		case "e", "edit":
			edited, err := editPremise(cfg, p)
			if err != nil {
				return err
			}
			p.Decision, p.Written = premiseDecisionEdited, edited
			return nil
		case "r", "reject":
			p.Decision = premiseDecisionRejected
			return nil
		}
	}
}

// proposedNotes keeps the fragment's existing notes, which accepting a draft
// never overwrites; the draft's notes are offered only where there are none.
func proposedNotes(p *premiseProposal) string {
	if p.Current.Notes != "" {
		return p.Current.Notes
	}
	return p.Draft.Notes
}

// premiseEditDoc is the document the author edits.
type premiseEditDoc struct {
	Premise *string `yaml:"premise"`
	Notes   string  `yaml:"notes"`
}

// editPremise opens the proposal in the editor as YAML — the critique as
// comments, which the parse drops — and reads back what the author saved.
func editPremise(cfg *config.Config, p *premiseProposal) (*premiseValues, error) {
	premise := p.Draft.Premise
	if premise == "" {
		premise = "NONE"
	}
	doc, err := yaml.Marshal(premiseEditDoc{Premise: &premise, Notes: proposedNotes(p)})
	if err != nil {
		return nil, fmt.Errorf("render premise for editing: %w", err)
	}
	edited, err := editInEditor(cfg, premiseEditHeader(p)+string(doc), "premise.yaml")
	if err != nil {
		return nil, fmt.Errorf("editor failed: %w", err)
	}
	return parseEditedPremise(edited)
}

// parseEditedPremise reads the saved edit document. NONE is always-load; a
// missing or blank premise is refused, never taken as always-load.
func parseEditedPremise(edited string) (*premiseValues, error) {
	var doc premiseEditDoc
	if err := yaml.Unmarshal([]byte(edited), &doc); err != nil {
		return nil, fmt.Errorf("edited premise is not valid YAML: %w", err)
	}
	if doc.Premise == nil || strings.TrimSpace(*doc.Premise) == "" {
		return nil, errPremiseEditEmpty
	}
	premise := strings.TrimSpace(*doc.Premise)
	if premise == "NONE" {
		premise = ""
	}
	return &premiseValues{Premise: premise, Notes: strings.TrimSpace(doc.Notes)}, nil
}

// premiseEditHeader is the comment block above the editable document: how to
// say always-load, then the critique findings to weigh.
func premiseEditHeader(p *premiseProposal) string {
	var b strings.Builder
	b.WriteString("# Edit the premise and notes, then save. Lines starting with # are dropped.\n")
	b.WriteString("# Always-load is the literal NONE; an empty premise is refused.\n")
	if p.Draft.SplitHint != "" {
		b.WriteString("#\n# SPLIT SUGGESTED:\n")
		writeCommented(&b, p.Draft.SplitHint)
	}
	if p.Critique != nil {
		for _, f := range p.Critique.Findings {
			fmt.Fprintf(&b, "#\n# finding [%s]\n", f.Kind)
			writeCommented(&b, findingText(f))
		}
	}
	b.WriteString("\n")
	return b.String()
}

func writeCommented(b *strings.Builder, text string) {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		b.WriteString("#   " + line + "\n")
	}
}

func findingText(f operations.PremiseFinding) string {
	var parts []string
	for _, kv := range [][2]string{{"", f.Evidence}, {"counterexample: ", f.Counterexample}, {"overlaps with: ", f.OverlapsWith}, {"patch: ", f.Patch}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+kv[1])
		}
	}
	return strings.Join(parts, "\n")
}

// renderPremiseProposal prints the current values, the draft, the split hint
// (first and loud: a fragment doing two jobs needs splitting, not a premise)
// and the findings. Every value is model- or author-written, so it goes
// through termsafe.
func renderPremiseProposal(w io.Writer, p *premiseProposal) {
	safe := func(s string) string { return termsafe.Sanitize(s, 0, true).Text }
	orNone := func(s string) string {
		if s == "" {
			return "NONE (always loads)"
		}
		return safe(s)
	}
	fmt.Fprintf(w, "Fragment: %s\n", termsafe.Field(p.Ref))
	if p.Draft.SplitHint != "" {
		fmt.Fprintf(w, "\n!! SPLIT SUGGESTED: this fragment may be doing more than one job.\n%s\n", safe(p.Draft.SplitHint))
	}
	fmt.Fprintf(w, "\nCurrent premise: %s\n", orNone(p.Current.Premise))
	if p.Current.Notes != "" {
		fmt.Fprintf(w, "Current notes:\n%s\n", safe(p.Current.Notes))
	}
	fmt.Fprintf(w, "\nDraft premise: %s\n", orNone(p.Draft.Premise))
	writeList(w, "Fires on", p.Draft.Moments, safe)
	writeList(w, "Not for", p.Draft.NotFor, safe)
	if p.Draft.Notes != "" {
		fmt.Fprintf(w, "Draft notes:\n%s\n", safe(p.Draft.Notes))
	}
	if p.Critique == nil {
		return
	}
	fmt.Fprintf(w, "\nCritique: %d %s\n", len(p.Critique.Findings), plural(len(p.Critique.Findings), "finding", "findings"))
	for _, f := range p.Critique.Findings {
		fmt.Fprintf(w, "  [%s]\n", termsafe.Field(string(f.Kind)))
		for _, line := range strings.Split(findingText(f), "\n") {
			if line != "" {
				fmt.Fprintf(w, "    %s\n", safe(line))
			}
		}
	}
}

func writeList(w io.Writer, title string, items []string, safe func(string) string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(w, "%s:\n", title)
	for _, it := range items {
		fmt.Fprintf(w, "  - %s\n", safe(it))
	}
}
