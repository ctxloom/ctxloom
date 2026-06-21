package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// ItemType distinguishes between fragments and prompts.
type ItemType string

const (
	ItemTypeFragment ItemType = "fragment"
	ItemTypePrompt   ItemType = "prompt"
)

// itemPrefix returns the prefix used in references (e.g., "fragments/" or "prompts/").
func (t ItemType) prefix() string {
	return string(t) + "s/"
}

// parseItemRef parses a reference like "bundle#fragments/name" or "bundle#prompts/name".
func parseItemRef(ref string, itemType ItemType) (bundleName, itemName string, err error) {
	hashIdx := strings.Index(ref, "#")
	if hashIdx == -1 {
		return "", "", fmt.Errorf("invalid reference format: expected bundle#%sname (got %q)", itemType.prefix(), ref)
	}

	bundleName = ref[:hashIdx]
	itemPath := ref[hashIdx+1:]

	prefix := itemType.prefix()
	if !strings.HasPrefix(itemPath, prefix) {
		return "", "", fmt.Errorf("invalid reference format: expected bundle#%sname (got %q)", prefix, ref)
	}

	itemName = strings.TrimPrefix(itemPath, prefix)
	if itemName == "" {
		return "", "", fmt.Errorf("invalid reference: missing %s name", itemType)
	}

	return bundleName, itemName, nil
}

// itemRow is the normalized listing shape shared by the fragment and prompt
// listings: a name, its merged tags, and the bundle it came from (the grouping
// key). It flattens the operations FragmentEntry / PromptEntry projections so
// the grouping/printing logic is type-agnostic.
type itemRow struct {
	Name   string
	Tags   []string
	Bundle string
}

// listItemRows returns every item of the given type via the operations
// read-path, grouped by bundle (SortBy:"source", since an entry's Source is its
// bundle name).
func listItemRows(cfg *config.Config, itemType ItemType) ([]itemRow, error) {
	ctx := context.Background()
	switch itemType {
	case ItemTypeFragment:
		res, err := operations.ListFragments(ctx, cfg, operations.ListFragmentsRequest{SortBy: "source"})
		if err != nil {
			return nil, err
		}
		rows := make([]itemRow, 0, len(res.Fragments))
		for _, f := range res.Fragments {
			rows = append(rows, itemRow{Name: f.Name, Tags: f.Tags, Bundle: f.Source})
		}
		return rows, nil
	case ItemTypePrompt:
		res, err := operations.ListPrompts(ctx, cfg, operations.ListPromptsRequest{SortBy: "source"})
		if err != nil {
			return nil, err
		}
		rows := make([]itemRow, 0, len(res.Prompts))
		for _, p := range res.Prompts {
			rows = append(rows, itemRow{Name: p.Name, Tags: p.Tags, Bundle: p.Source})
		}
		return rows, nil
	}
	return nil, nil
}

// filterByBundle keeps only the items belonging to bundleFilter (or all when
// the filter is empty).
func filterByBundle(rows []itemRow, bundleFilter string) []itemRow {
	if bundleFilter == "" {
		return rows
	}
	var filtered []itemRow
	for _, r := range rows {
		if r.Bundle == bundleFilter {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// printItemInfos prints items grouped by bundle, with tags.
func printItemInfos(rows []itemRow, itemType ItemType) {
	fmt.Printf("%ss (%d):\n\n", titleCase(string(itemType)), len(rows))
	currentBundle := ""
	for _, r := range rows {
		if r.Bundle != currentBundle {
			if currentBundle != "" {
				fmt.Println()
			}
			fmt.Printf("  %s:\n", r.Bundle)
			currentBundle = r.Bundle
		}
		fmt.Printf("    - %s", r.Name)
		if len(r.Tags) > 0 {
			fmt.Printf(" [%s]", strings.Join(r.Tags, ", "))
		}
		fmt.Println()
	}
}

// listItems lists all items of the given type, optionally filtered by bundle.
func listItems(cmd *cobra.Command, itemType ItemType, bundleFilter string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	rows, err := listItemRows(cfg, itemType)
	if err != nil {
		return fmt.Errorf("failed to list %ss: %w", itemType, err)
	}

	filtered := filterByBundle(rows, bundleFilter)
	if filtered == nil {
		filtered = []itemRow{}
	}
	return emit(cmd, filtered, func() error {
		if len(rows) == 0 {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "No %ss found.\n", itemType)
			fmt.Fprintln(out, "Add remote bundles to a profile (ctxloom profile create/modify), then ctxloom remote pull")
			return nil
		}
		printItemInfos(filtered, itemType)
		return nil
	})
}

// loadBundleForItem resolves an item reference and loads its bundle, returning
// the bundle, the item name, and the loaded config (needed by distill).
func loadBundleForItem(ref string, itemType ItemType) (*bundles.Bundle, string, *config.Config, error) {
	bundleName, itemName, err := parseItemRef(ref, itemType)
	if err != nil {
		return nil, "", nil, err
	}

	cfg, err := GetConfig()
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to load config: %w", err)
	}

	bundle, err := operations.GetBundle(cfg, bundleName)
	if err != nil {
		return nil, "", nil, fmt.Errorf("bundle not found: %s", bundleName)
	}
	return bundle, itemName, cfg, nil
}

// itemDisplayContent returns the content and distilled text for an item,
// erroring (with the available-names list) when it doesn't exist.
func itemDisplayContent(bundle *bundles.Bundle, itemName string, itemType ItemType) (content, distilled string, err error) {
	switch itemType {
	case ItemTypeFragment:
		frag, exists := bundle.Fragments[itemName]
		if !exists {
			return "", "", fmt.Errorf("fragment not found: %s\n\nAvailable fragments: %s",
				itemName, strings.Join(bundle.FragmentNames(), ", "))
		}
		return frag.Content, frag.Distilled, nil
	case ItemTypePrompt:
		prompt, exists := bundle.Prompts[itemName]
		if !exists {
			return "", "", fmt.Errorf("prompt not found: %s\n\nAvailable prompts: %s",
				itemName, strings.Join(bundle.PromptNames(), ", "))
		}
		return prompt.Content, prompt.Distilled, nil
	}
	return "", "", nil
}

// showItem displays the content of a specific item.
func showItem(ref string, itemType ItemType, showDistilled bool) error {
	bundle, itemName, _, err := loadBundleForItem(ref, itemType)
	if err != nil {
		return err
	}

	content, distilled, err := itemDisplayContent(bundle, itemName, itemType)
	if err != nil {
		return err
	}

	if showDistilled && distilled != "" {
		content = distilled
		fmt.Println("# (distilled version)")
	}

	fmt.Printf("%s\n\n", itemName)
	fmt.Print(content)
	if !strings.HasSuffix(content, "\n") {
		fmt.Println()
	}

	return nil
}

// createItem creates a new item in a bundle. Thin wrapper over the
// frontend-agnostic operations.AddItem core.
func createItem(bundleName, itemName string, itemType ItemType) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	_, err = operations.AddItem(context.Background(), cfg, operations.AddItemRequest{
		Bundle:  bundleName,
		Kind:    operations.ItemKind(itemType),
		Name:    itemName,
		Content: "# " + itemName + "\n\nAdd content here.",
	})
	if err != nil {
		if errors.Is(err, operations.ErrItemExists) {
			return fmt.Errorf("%s already exists: %s", itemType, itemName)
		}
		return err
	}

	fmt.Printf("Created %s %q in bundle %q\n", itemType, itemName, bundleName)
	fmt.Printf("Edit with: ctxloom %s edit %s#%s%s\n", itemType, bundleName, itemType.prefix(), itemName)
	return nil
}

// deleteItem removes an item from a bundle. Thin wrapper over
// operations.DeleteItem.
func deleteItem(ref string, itemType ItemType) error {
	bundleName, itemName, err := parseItemRef(ref, itemType)
	if err != nil {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	_, err = operations.DeleteItem(context.Background(), cfg, operations.DeleteItemRequest{
		Bundle: bundleName,
		Kind:   operations.ItemKind(itemType),
		Name:   itemName,
	})
	if err != nil {
		if errors.Is(err, operations.ErrItemNotFound) {
			return fmt.Errorf("%s not found: %s", itemType, itemName)
		}
		return err
	}

	fmt.Printf("Deleted %s %q from bundle %q\n", itemType, itemName, bundleName)
	return nil
}

// editItem opens an item in the editor, then writes the new content back through
// the operations core. The $EDITOR round-trip is the only CLI-specific concern;
// the load, guard, distillation, and save all live in the library.
func editItem(ref string, itemType ItemType) error {
	bundleName, itemName, err := parseItemRef(ref, itemType)
	if err != nil {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	kind := operations.ItemKind(itemType)

	cur, err := operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{
		Bundle: bundleName,
		Kind:   kind,
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
		fmt.Println("No changes made.")
		return nil
	}

	res, err := operations.SetItemContent(context.Background(), cfg, operations.SetItemContentRequest{
		Bundle:    bundleName,
		Kind:      kind,
		Name:      itemName,
		Content:   newContent,
		Distiller: newLLMDistiller(cfg),
	})
	if err != nil {
		return err
	}

	fmt.Printf("Updated %s %q in bundle %q", itemType, itemName, bundleName)
	if res.Distilled {
		fmt.Print(" (re-distilled)")
	}
	fmt.Println()
	printPushReminder(bundleName)
	return nil
}

// distillSource returns the content and distill flags for an item, erroring
// when it doesn't exist.
// distillItem (re)distills an item via the operations core; the CLI only
// supplies the LLM-backed Distiller and renders the outcome.
func distillItem(ref string, itemType ItemType, force bool) error {
	bundleName, itemName, err := parseItemRef(ref, itemType)
	if err != nil {
		return err
	}
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	res, err := operations.DistillItem(context.Background(), cfg, operations.DistillItemRequest{
		Bundle:    bundleName,
		Kind:      operations.ItemKind(itemType),
		Name:      itemName,
		Force:     force,
		Distiller: newLLMDistiller(cfg),
	})
	if err != nil {
		if errors.Is(err, operations.ErrItemNotFound) {
			return fmt.Errorf("%s not found: %s", itemType, itemName)
		}
		return err
	}

	if res.Status == "skipped" {
		switch res.Reason {
		case "no_distill":
			fmt.Printf("%s %q is marked as no_distill\n", titleCase(string(itemType)), itemName)
		case "unchanged":
			fmt.Printf("%s %q is already distilled and unchanged\n", titleCase(string(itemType)), itemName)
		case "no_distiller":
			fmt.Printf("no LLM plugin configured; cannot distill %s %q\n", itemType, itemName)
		}
		return nil
	}

	fmt.Printf("Distilled %s (%s)\n", itemName, res.ModelID)
	return nil
}

// pushBundle publishes the named bundle to a remote. Each step is an operations
// call — resolve the bundle path, resolve the target remote (an explicit
// override or inferred from the bundle's location), then publish — so the CLI
// re-implements none of the push logic and the same path is reachable by any
// frontend.
func pushBundle(cmd *cobra.Command, bundleName, remoteOverride string, createPR bool, message string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundle, err := operations.GetBundle(cfg, bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	remoteName, err := operations.ResolveBundleRemote(cfg, bundle.Path, remoteOverride)
	if err != nil {
		return err
	}

	result, err := operations.PushBundle(cmd.Context(), cfg, operations.PushBundleRequest{
		Path:     bundle.Path,
		Remote:   remoteName,
		Message:  message,
		CreatePR: createPR,
	})
	if err != nil {
		return err
	}

	return emit(cmd, result, func() error { return printPushResult(cmd.OutOrStdout(), result) })
}

// printPushResult renders a push outcome for humans.
func printPushResult(w io.Writer, r *operations.PushBundleResult) error {
	if r.Status == "pr-created" {
		_, err := fmt.Fprintf(w, "Created pull request: %s\n", r.PRURL)
		return err
	}
	if _, err := fmt.Fprintf(w, "Pushed %s to %s\n", r.TargetPath, r.Remote); err != nil {
		return err
	}
	if r.CommitSHA != "" {
		if _, err := fmt.Fprintf(w, "Commit: %s\n", shortSHA(r.CommitSHA)); err != nil {
			return err
		}
	}
	return nil
}
