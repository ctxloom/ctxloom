package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// The per-item command bodies behind `fragment|command
// show|create|remove|edit|distill`. Each is a thin frontend over an operations
// call: argument parsing, the $EDITOR round-trip, and rendering — no bundle IO.

// showItem displays the content of a specific item. With interactive set AND an
// interactive terminal, it then offers the TR4 trust review/marking surface; in
// any non-interactive context (piped, redirected, scripted, or -i unset) it
// behaves exactly as before — the content is written to cmd.OutOrStdout (os.Stdout
// by default) with no trust UI, so piped output is byte-for-byte unchanged.
//
// The read is operations.GetItemContent: one bundle resolution, one item lookup,
// one not-found message for every frontend. Doing it here instead (load the
// bundle, switch on the kind, pick content+distilled) is what let `show` and
// `edit` disagree about which bundle refs exist.
func showItem(cmd *cobra.Command, ref string, itemType ItemType, showDistilled, interactive bool) error {
	bundleName, itemName, err := itemRefTarget(ref, itemType)
	if err != nil {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	item, err := operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{
		Bundle: bundleName,
		Kind:   itemType,
		Name:   itemName,
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	content := item.Content
	if showDistilled && item.Distilled != "" {
		content = item.Distilled
		fmt.Fprintln(out, "# (distilled version)")
	}

	fmt.Fprintf(out, "%s\n\n", itemName)
	fmt.Fprint(out, content)
	if !strings.HasSuffix(content, "\n") {
		fmt.Fprintln(out)
	}

	// TTY-gated interactive trust review (-i). The content above is emitted
	// identically whether or not -i is set, and all trust UI goes to stderr, so
	// non-interactive/piped output never sees the trust surface. Viewing never
	// trusts — only an explicit t/b choice mutates.
	if interactive && isInteractiveTerminal() {
		return offerItemTrust(cmd, cfg, ref)
	}
	return nil
}

// createItem creates a new item in a bundle. Thin wrapper over the
// frontend-agnostic operations.AddItem core, rendered through emit() so the
// global --format is honoured and the human lines go to cmd.OutOrStdout()
// rather than the process's real stdout.
func createItem(cmd *cobra.Command, bundleName, itemName string, itemType ItemType) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	res, err := operations.AddItem(context.Background(), cfg, operations.AddItemRequest{
		Bundle:  bundleName,
		Kind:    itemType,
		Name:    itemName,
		Content: "# " + itemName + "\n\nAdd content here.",
	})
	if err != nil {
		if errors.Is(err, operations.ErrItemExists) {
			return fmt.Errorf("%s already exists: %s", itemType, itemName)
		}
		return err
	}

	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Created %s %q in bundle %q\n", itemType, itemName, bundleName)
		fmt.Fprintf(out, "Edit with: ctxloom %s edit %s#%s%s\n", itemType, bundleName, itemRefPrefix(itemType), itemName)
		return nil
	})
}

// removeItem removes an item from a bundle. Bare (yes == false) reports what
// would be removed and destroys nothing, exiting 0; yes == true performs the
// removal via operations.DeleteItem. Both paths render through emit() so the
// report and the applied result both honour --format identically.
func removeItem(cmd *cobra.Command, ref string, itemType ItemType, yes bool) error {
	bundleName, itemName, err := itemRefTarget(ref, itemType)
	if err != nil {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if !yes {
		// Confirm the item exists before reporting: a preview naming a target
		// that isn't there would be worse than the not-found error below.
		if _, err := operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{
			Bundle: bundleName, Kind: itemType, Name: itemName,
		}); err != nil {
			if errors.Is(err, operations.ErrItemNotFound) {
				return fmt.Errorf("%s not found: %s", itemType, itemName)
			}
			return err
		}

		target := fmt.Sprintf("%s %q from bundle %q", itemType, itemName, bundleName)
		applyCmd := fmt.Sprintf("ctxloom %s remove %s --yes", itemType, ref)
		return emit(cmd, newRemovePreviewResult(target, nil, applyCmd), func() error {
			printRemovePreview(cmd.OutOrStdout(), target, nil, applyCmd)
			return nil
		})
	}

	res, err := operations.DeleteItem(context.Background(), cfg, operations.DeleteItemRequest{
		Bundle: bundleName,
		Kind:   itemType,
		Name:   itemName,
	})
	if err != nil {
		if errors.Is(err, operations.ErrItemNotFound) {
			return fmt.Errorf("%s not found: %s", itemType, itemName)
		}
		return err
	}

	return emit(cmd, res, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed %s %q from bundle %q\n", itemType, itemName, bundleName)
		return nil
	})
}

// editItem opens an item in the editor, then writes the new content back through
// the operations core. The $EDITOR round-trip is the only CLI-specific concern;
// the load, guard, distillation, and save all live in the library.
//
// noDistill is the per-invocation `--no-distill` opt-out (distinct from the
// item's own persistent no_distill field): when set, no Distiller is passed to
// SetItemContent, so the LLM is never called for this edit. The stale
// distilled form is not left behind describing the old content — the
// operations core (applyFragmentEdits/applyPromptEdits) already clears
// Distilled/DistilledBy/ContentHash unconditionally the instant raw content
// changes, before it even decides whether to redistill — so a skipped
// distillation leaves the item with NO distilled form (falls back to raw in
// distilled-mode assembly) rather than a WRONG one. See docs at those
// functions for the invariant this relies on.
func editItem(cmd *cobra.Command, ref string, itemType ItemType, noDistill bool) error {
	bundleName, itemName, err := itemRefTarget(ref, itemType)
	if err != nil {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	cur, err := operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{
		Bundle: bundleName,
		Kind:   itemType,
		Name:   itemName,
	})
	if err != nil {
		return err
	}

	newContent, err := editInEditor(cfg, cur.Content, itemName+".md")
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}
	if newContent == cur.Content {
		return emit(cmd, &operations.SetItemContentResult{
			Status: "unchanged", Bundle: bundleName, Name: itemName,
		}, func() error {
			fmt.Fprintln(cmd.OutOrStdout(), "No changes made.")
			return nil
		})
	}
	if err := checkEditedContent(itemType, itemName, newContent); err != nil {
		return err
	}

	distiller, err := distillerForEdit(cfg, noDistill)
	if err != nil {
		return refuseWithheldDistillPrompt(cmd, err)
	}

	res, err := operations.SetItemContent(context.Background(), cfg, operations.SetItemContentRequest{
		Bundle:    bundleName,
		Kind:      itemType,
		Name:      itemName,
		Content:   newContent,
		Distiller: distiller,
	})
	if err != nil {
		return err
	}

	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Updated %s %q in bundle %q", itemType, itemName, bundleName)
		if res.Distilled {
			fmt.Fprint(out, " (re-distilled)")
		}
		fmt.Fprintln(out)
		fmt.Fprint(out, editNoDistillWarning(itemType, ref, noDistill, cur.NoDistill))
		printPushReminder(out, bundleName)
		return nil
	})
}

// distillerForEdit resolves which Distiller SetItemContent should use for an
// edit: nil (skip distillation entirely) when --no-distill was passed, the
// real LLM-backed one otherwise. Split out so the flag's effect — nil vs. a
// live distiller — is a plain, fast unit-testable decision, independent of
// whatever backend newLLMDistiller resolves to.
//
// --no-distill short-circuits BEFORE the prompt is resolved: an edit that asked
// for no distillation cannot be refused over a distill prompt it will never
// use. The error arm is the withheld-prompt refusal only.
func distillerForEdit(cfg *config.Config, noDistill bool) (operations.Distiller, error) {
	if noDistill {
		return nil, nil
	}
	return newLLMDistiller(cfg)
}

// editNoDistillWarning returns the exact line to print when --no-distill
// skipped a distillation that would otherwise have run, or "" when no warning
// is warranted: the flag wasn't passed, or the item was already marked
// (persistent) no_distill, in which case the flag changed nothing — the item
// was never going to auto-distill, so pointing at `... distill <ref>` (which
// would itself just report "marked as no_distill" and skip) would be noise,
// not signal. The distilled form is never left stale-but-present after this:
// applyFragmentEdits/applyPromptEdits already clear Distilled/DistilledBy/
// ContentHash unconditionally the instant raw content changes, so skipping
// distillation here leaves the item with NO distilled form (raw content is
// served in distilled-mode assembly) rather than a WRONG one.
func editNoDistillWarning(itemType ItemType, ref string, noDistillFlag, wasAlreadyNoDistill bool) string {
	if !noDistillFlag || wasAlreadyNoDistill {
		return ""
	}
	return fmt.Sprintf("warning: distilled form not refreshed (--no-distill); run `ctxloom %s distill %s` before relying on distilled-mode output\n", itemType, ref)
}

// distillItem (re)distills an item via the operations core; the CLI only
// supplies the LLM-backed Distiller and renders the outcome, through emit() so
// the global --format is honoured.
func distillItem(cmd *cobra.Command, ref string, itemType ItemType, force bool) error {
	bundleName, itemName, err := itemRefTarget(ref, itemType)
	if err != nil {
		return err
	}
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	distiller, err := newLLMDistiller(cfg)
	if err != nil {
		return refuseWithheldDistillPrompt(cmd, err)
	}

	res, err := operations.DistillItem(context.Background(), cfg, operations.DistillItemRequest{
		Bundle:    bundleName,
		Kind:      itemType,
		Name:      itemName,
		Force:     force,
		Distiller: distiller,
	})
	if err != nil {
		if errors.Is(err, operations.ErrItemNotFound) {
			return fmt.Errorf("%s not found: %s", itemType, itemName)
		}
		return err
	}

	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		if res.Status == "skipped" {
			switch res.Reason {
			case "no_distill":
				fmt.Fprintf(out, "%s %q is marked as no_distill\n", titleCase(string(itemType)), itemName)
			case "unchanged":
				fmt.Fprintf(out, "%s %q is already distilled and unchanged\n", titleCase(string(itemType)), itemName)
			case "no_distiller":
				fmt.Fprintf(out, "no LLM plugin configured; cannot distill %s %q\n", itemType, itemName)
			}
			return nil
		}
		fmt.Fprintf(out, "Distilled %s (%s)\n", itemName, res.ModelID)
		return nil
	})
}

// checkEditedContent rejects an editor buffer that came back empty.
// editItem's only guard was "did it change?", so an editor that crashed, wrote
// a truncated file, or never wrote at all replaced the item's entire body with
// nothing — and the command printed "Updated <type> ..." and exited 0.
// editInEditor returns whatever bytes are on disk with no length check, and
// operations.SetItemContent validates only the kind and the item's existence.
// An intentional blank is not a thing anyone does through this path; losing a
// fragment to a wrapper script that silently failed very much is.
func checkEditedContent(itemType ItemType, itemName, edited string) error {
	if strings.TrimSpace(edited) == "" {
		return fmt.Errorf("refusing to overwrite %s %q with an empty buffer — the editor returned no content (crash, truncated write, or a wrapper that never saved); the item is unchanged", itemType, itemName)
	}
	return nil
}

// checkBundleFilter tells a MISTYPED --bundle from one that legitimately holds
// no items of this kind. The empty-listing branch used to test the
// UNFILTERED row slice, which is non-empty whenever any item exists anywhere, so
// `fragment list --bundle nosuchbundle` printed "Fragments (0):" and exited 0
// with nothing to suggest the name was wrong.
//
// known is the set of bundle names that actually exist. An EMPTY known set means
// the lookup itself did not answer (see runListItems), and this stays silent
// rather than inventing a verdict from a failed enumeration.
func checkBundleFilter(bundleFilter string, known []string) error {
	if bundleFilter == "" || len(known) == 0 {
		return nil
	}
	for _, name := range known {
		if name == bundleFilter {
			return nil
		}
	}
	return fmt.Errorf("no bundle named %q (bundles: %s)", bundleFilter, strings.Join(known, ", "))
}
