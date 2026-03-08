package cmd

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/SophisticatedContextManager/scm/internal/bundles"
	"github.com/SophisticatedContextManager/scm/internal/operations"
	"github.com/SophisticatedContextManager/scm/internal/remote"
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

// listItems lists all items of the given type, optionally filtered by bundle.
func listItems(itemType ItemType, bundleFilter string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)

	var infos []bundles.ContentInfo
	switch itemType {
	case ItemTypeFragment:
		infos, err = loader.ListAllFragments()
	case ItemTypePrompt:
		infos, err = loader.ListAllPrompts()
	}
	if err != nil {
		return fmt.Errorf("failed to list %ss: %w", itemType, err)
	}

	if len(infos) == 0 {
		fmt.Printf("No %ss found.\n", itemType)
		fmt.Printf("Install bundles with: scm %s install <remote>/bundle-name\n", itemType)
		return nil
	}

	// Filter by bundle if specified
	if bundleFilter != "" {
		var filtered []bundles.ContentInfo
		for _, info := range infos {
			if info.Bundle == bundleFilter {
				filtered = append(filtered, info)
			}
		}
		infos = filtered
	}

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

	return nil
}

// showItem displays the content of a specific item.
func showItem(ref string, itemType ItemType, showDistilled bool) error {
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

	var content, distilled string
	var available []string

	switch itemType {
	case ItemTypeFragment:
		frag, exists := bundle.Fragments[itemName]
		if !exists {
			return fmt.Errorf("fragment not found: %s\n\nAvailable fragments: %s",
				itemName, strings.Join(bundle.FragmentNames(), ", "))
		}
		content = frag.Content
		distilled = frag.Distilled
		available = bundle.FragmentNames()
	case ItemTypePrompt:
		prompt, exists := bundle.Prompts[itemName]
		if !exists {
			return fmt.Errorf("prompt not found: %s\n\nAvailable prompts: %s",
				itemName, strings.Join(bundle.PromptNames(), ", "))
		}
		content = prompt.Content
		distilled = prompt.Distilled
		available = bundle.PromptNames()
	}
	_ = available // Used in error messages above

	if showDistilled && distilled != "" {
		content = distilled
		fmt.Println("# (distilled version)")
	}

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
	fmt.Printf("Edit with: scm %s edit %s#%s%s\n", itemType, bundleName, itemType.prefix(), itemName)
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

// distillItem distills an item to create a token-efficient version.
func distillItem(ref string, itemType ItemType, force bool) error {
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

	var content string
	var noDistill, needsDistill bool

	switch itemType {
	case ItemTypeFragment:
		frag, exists := bundle.Fragments[itemName]
		if !exists {
			return fmt.Errorf("fragment not found: %s", itemName)
		}
		noDistill = frag.NoDistill
		needsDistill = frag.NeedsDistill()
		content = frag.Content
	case ItemTypePrompt:
		prompt, exists := bundle.Prompts[itemName]
		if !exists {
			return fmt.Errorf("prompt not found: %s", itemName)
		}
		noDistill = prompt.NoDistill
		needsDistill = prompt.NeedsDistill()
		content = prompt.Content
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

	result, err := remote.Publish(cmd.Context(), bundle.Path, remoteName, opts, registry, auth)
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
