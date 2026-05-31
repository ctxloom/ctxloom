package cmd

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
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

// listItemInfos lists every item of the given type via the loader.
func listItemInfos(loader *bundles.Loader, itemType ItemType) ([]bundles.ContentInfo, error) {
	switch itemType {
	case ItemTypeFragment:
		return loader.ListAllFragments()
	case ItemTypePrompt:
		return loader.ListAllPrompts()
	}
	return nil, nil
}

// filterByBundle keeps only the items belonging to bundleFilter (or all when
// the filter is empty).
func filterByBundle(infos []bundles.ContentInfo, bundleFilter string) []bundles.ContentInfo {
	if bundleFilter == "" {
		return infos
	}
	var filtered []bundles.ContentInfo
	for _, info := range infos {
		if info.Bundle == bundleFilter {
			filtered = append(filtered, info)
		}
	}
	return filtered
}

// printItemInfos prints items grouped by bundle, with tags.
func printItemInfos(infos []bundles.ContentInfo, itemType ItemType) {
	fmt.Printf("%ss (%d):\n\n", titleCase(string(itemType)), len(infos))
	currentBundle := ""
	for _, info := range infos {
		if info.Bundle != currentBundle {
			if currentBundle != "" {
				fmt.Println()
			}
			fmt.Printf("  %s:\n", info.Bundle)
			currentBundle = info.Bundle
		}
		fmt.Printf("    - %s", info.Name)
		if len(info.Tags) > 0 {
			fmt.Printf(" [%s]", strings.Join(info.Tags, ", "))
		}
		fmt.Println()
	}
}

// listItems lists all items of the given type, optionally filtered by bundle.
func listItems(itemType ItemType, bundleFilter string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	infos, err := listItemInfos(loader, itemType)
	if err != nil {
		return fmt.Errorf("failed to list %ss: %w", itemType, err)
	}

	if len(infos) == 0 {
		fmt.Printf("No %ss found.\n", itemType)
		fmt.Printf("Install bundles with: ctxloom %s install <remote>/bundle-name\n", itemType)
		return nil
	}

	printItemInfos(filterByBundle(infos, bundleFilter), itemType)
	return nil
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

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
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

// createItem creates a new item in a bundle.
func createItem(bundleName, itemName string, itemType ItemType) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	switch itemType {
	case ItemTypeFragment:
		if _, exists := bundle.Fragments[itemName]; exists {
			return fmt.Errorf("fragment already exists: %s", itemName)
		}
		if bundle.Fragments == nil {
			bundle.Fragments = make(map[string]bundles.BundleFragment)
		}
		bundle.Fragments[itemName] = bundles.BundleFragment{
			Content: "# " + itemName + "\n\nAdd content here.",
		}
	case ItemTypePrompt:
		if _, exists := bundle.Prompts[itemName]; exists {
			return fmt.Errorf("prompt already exists: %s", itemName)
		}
		if bundle.Prompts == nil {
			bundle.Prompts = make(map[string]bundles.BundlePrompt)
		}
		bundle.Prompts[itemName] = bundles.BundlePrompt{
			Content: "# " + itemName + "\n\nAdd content here.",
		}
	}

	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf("Created %s %q in bundle %q\n", itemType, itemName, bundleName)
	fmt.Printf("Edit with: ctxloom %s edit %s#%s%s\n", itemType, bundleName, itemType.prefix(), itemName)
	return nil
}

// deleteItem removes an item from a bundle.
func deleteItem(ref string, itemType ItemType) error {
	bundleName, itemName, err := parseItemRef(ref, itemType)
	if err != nil {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	switch itemType {
	case ItemTypeFragment:
		if _, exists := bundle.Fragments[itemName]; !exists {
			return fmt.Errorf("fragment not found: %s", itemName)
		}
		delete(bundle.Fragments, itemName)
	case ItemTypePrompt:
		if _, exists := bundle.Prompts[itemName]; !exists {
			return fmt.Errorf("prompt not found: %s", itemName)
		}
		delete(bundle.Prompts, itemName)
	}

	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf("Deleted %s %q from bundle %q\n", itemType, itemName, bundleName)
	return nil
}

// editItem opens an item for editing.
func editItem(ref string, itemType ItemType) error {
	bundleName, itemName, err := parseItemRef(ref, itemType)
	if err != nil {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	switch itemType {
	case ItemTypeFragment:
		return editFragment(cfg, bundle, itemName)
	case ItemTypePrompt:
		return editPrompt(cfg, bundle, itemName)
	}

	return nil
}

// distillSource returns the content and distill flags for an item, erroring
// when it doesn't exist.
func distillSource(bundle *bundles.Bundle, itemName string, itemType ItemType) (content string, noDistill, needsDistill bool, err error) {
	switch itemType {
	case ItemTypeFragment:
		frag, exists := bundle.Fragments[itemName]
		if !exists {
			return "", false, false, fmt.Errorf("fragment not found: %s", itemName)
		}
		return frag.Content, frag.NoDistill, frag.NeedsDistill(), nil
	case ItemTypePrompt:
		prompt, exists := bundle.Prompts[itemName]
		if !exists {
			return "", false, false, fmt.Errorf("prompt not found: %s", itemName)
		}
		return prompt.Content, prompt.NoDistill, prompt.NeedsDistill(), nil
	}
	return "", false, false, nil
}

// applyDistilled writes a distilled result (and its model + content hash) back
// onto the named item in the bundle.
func applyDistilled(bundle *bundles.Bundle, itemName string, itemType ItemType, distilled, modelID string) {
	switch itemType {
	case ItemTypeFragment:
		frag := bundle.Fragments[itemName]
		frag.Distilled = distilled
		frag.DistilledBy = modelID
		frag.ContentHash = frag.ComputeContentHash()
		bundle.Fragments[itemName] = frag
	case ItemTypePrompt:
		prompt := bundle.Prompts[itemName]
		prompt.Distilled = distilled
		prompt.DistilledBy = modelID
		prompt.ContentHash = prompt.ComputeContentHash()
		bundle.Prompts[itemName] = prompt
	}
}

// distillItem distills an item to create a token-efficient version.
func distillItem(ref string, itemType ItemType, force bool) error {
	bundle, itemName, cfg, err := loadBundleForItem(ref, itemType)
	if err != nil {
		return err
	}

	content, noDistill, needsDistill, err := distillSource(bundle, itemName, itemType)
	if err != nil {
		return err
	}

	if noDistill {
		fmt.Printf("%s %q is marked as no_distill\n", titleCase(string(itemType)), itemName)
		return nil
	}

	if !force && !needsDistill {
		fmt.Printf("%s %q is already distilled and unchanged\n", titleCase(string(itemType)), itemName)
		return nil
	}

	fmt.Printf("Distilling %s...", itemName)

	distillPrompt, err := loadDistillPrompt()
	if err != nil {
		return fmt.Errorf("failed to load distill prompt: %w", err)
	}

	pluginName := cfg.GetDefaultLLMPlugin()
	pluginCfg := cfg.LM.Plugins[pluginName]
	siblingCtx := buildSiblingContext(bundle, itemType.prefix()+itemName)

	distilled, modelID, err := distillWithModel(pluginName, pluginCfg.Env, itemName, content, distillPrompt, siblingCtx)
	if err != nil {
		fmt.Printf(" FAILED: %v\n", err)
		return err
	}

	applyDistilled(bundle, itemName, itemType, distilled, modelID)

	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf(" done (%s)\n", modelID)
	return nil
}

// installBundle installs a bundle from a remote.
func installBundle(cmd *cobra.Command, reference string, force, blind bool) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	result, err := operations.PullItem(cmd.Context(), cfg, operations.PullItemRequest{
		Reference: reference,
		ItemType:  "bundle",
		Force:     force,
		Blind:     blind,
	})
	if err != nil {
		return err
	}

	action := "Installed"
	if result.Overwritten {
		action = "Updated"
	}

	fmt.Printf("%s bundle: %s\n", action, result.LocalPath)
	fmt.Printf("SHA: %s\n", result.SHA[:7])

	return nil
}

// pushBundle pushes a bundle to a remote.
func pushBundle(cmd *cobra.Command, bundleName, remoteName string, createPR bool, branch, message string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	registry, err := remote.NewRegistry("")
	if err != nil {
		return fmt.Errorf("failed to initialize registry: %w", err)
	}

	if remoteName == "" {
		remoteName = registry.GetDefault()
		if remoteName == "" {
			return fmt.Errorf("no remote specified and no default set")
		}
	}

	auth := remote.LoadAuth("")

	opts := remote.PublishOptions{
		CreatePR: createPR,
		Branch:   branch,
		Message:  message,
		ItemType: remote.ItemTypeBundle,
	}

	fmt.Printf("Publishing bundle %q to %s...\n", bundleName, remoteName)

	pm := remote.NewPublishManager(registry, auth)
	result, err := pm.Publish(cmd.Context(), bundle.Path, remoteName, opts)
	if err != nil {
		return err
	}

	if result.PRURL != "" {
		fmt.Printf("Created pull request: %s\n", result.PRURL)
	} else {
		action := "Created"
		if !result.Created {
			action = "Updated"
		}
		fmt.Printf("%s %s\n", action, result.Path)
		fmt.Printf("Commit: %s\n", result.SHA[:7])
	}

	return nil
}
