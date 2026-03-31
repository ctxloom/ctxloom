package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/profiles"
)

var editCmd = &cobra.Command{
	Use:    "edit <reference>",
	Short:  "Edit bundles, profiles, fragments, or prompts",
	Hidden: true, // Use 'ctxloom fragment edit', 'ctxloom prompt edit', or 'ctxloom profile edit'
	Long: `Edit content using your configured editor.

Reference formats:
  bundle <name>               Edit a bundle's YAML file
  profile <name>              Edit a profile's YAML file
  <bundle>#fragments/<name>   Edit a fragment's content
  <bundle>#prompts/<name>     Edit a prompt's content

After editing, you may want to push changes:
  ctxloom bundle push <name> [remote]
  ctxloom profile push <name> [remote]

Examples:
  ctxloom edit bundle my-bundle
  ctxloom edit profile my-profile
  ctxloom edit my-bundle#fragments/coding-standards
  ctxloom edit my-bundle#prompts/code-review`,
	Args: cobra.MinimumNArgs(1),
	RunE: runEdit,
}

func runEdit(cmd *cobra.Command, args []string) error {
	// Handle "edit bundle <name>" and "edit profile <name>"
	if len(args) >= 2 {
		switch args[0] {
		case "bundle":
			return editBundleFile(args[1])
		case "profile":
			return editProfileFile(args[1])
		}
	}

	ref := args[0]

	// Parse reference: bundle#type/name
	hashIdx := strings.Index(ref, "#")
	if hashIdx == -1 {
		return fmt.Errorf("invalid reference format\n\nUsage:\n  ctxloom edit bundle <name>                    # Edit bundle YAML\n  ctxloom edit profile <name>                   # Edit profile YAML\n  ctxloom edit <bundle>#fragments/<name>        # Edit fragment content\n  ctxloom edit <bundle>#prompts/<name>          # Edit prompt content")
	}

	bundleName := ref[:hashIdx]
	itemPath := ref[hashIdx+1:]

	// Parse item path: type/name
	parts := strings.SplitN(itemPath, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid item path: expected <type>/<name> (got %q)", itemPath)
	}
	itemType := parts[0]
	itemName := parts[1]

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
	case "fragments":
		return editFragment(cfg, bundle, itemName)
	case "prompts":
		return editPrompt(cfg, bundle, itemName)
	default:
		return fmt.Errorf("unknown item type: %s (expected 'fragments' or 'prompts')", itemType)
	}
}

// editBundleFile opens a bundle's YAML file in the editor.
func editBundleFile(name string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(name)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", name)
	}

	// Read current content
	content, err := os.ReadFile(bundle.Path)
	if err != nil {
		return fmt.Errorf("failed to read bundle: %w", err)
	}

	// Edit in editor
	newContent, err := editInEditor(cfg, string(content), filepath.Base(bundle.Path))
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	if newContent == string(content) {
		fmt.Println("No changes made.")
		return nil
	}

	// Write back
	if err := os.WriteFile(bundle.Path, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf("Updated bundle: %s\n", bundle.Path)
	return nil
}

// editProfileFile opens a profile's YAML file in the editor.
func editProfileFile(name string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
	if len(profileDirs) == 0 {
		return fmt.Errorf("no profiles directory found")
	}

	loader := profiles.NewLoader(profileDirs)
	profile, err := loader.Load(name)
	if err != nil {
		return fmt.Errorf("profile not found: %s", name)
	}

	// Read current content
	content, err := os.ReadFile(profile.Path)
	if err != nil {
		return fmt.Errorf("failed to read profile: %w", err)
	}

	// Edit in editor
	newContent, err := editInEditor(cfg, string(content), filepath.Base(profile.Path))
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	if newContent == string(content) {
		fmt.Println("No changes made.")
		return nil
	}

	// Write back
	if err := os.WriteFile(profile.Path, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to save profile: %w", err)
	}

	fmt.Printf("Updated profile: %s\n", profile.Path)
	return nil
}

func editFragment(cfg interface{ GetDefaultLLMPlugin() string }, bundle *bundles.Bundle, fragName string) error {
	frag, exists := bundle.Fragments[fragName]
	if !exists {
		return fmt.Errorf("fragment not found: %s\n\nAvailable fragments: %s", fragName, strings.Join(bundle.FragmentNames(), ", "))
	}

	// Get full config for editor and plugin
	fullCfg, err := GetConfig()
	if err != nil {
		return err
	}

	// Edit content using editor
	newContent, err := editInEditor(fullCfg, frag.Content, fragName+".md")
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	if newContent == frag.Content {
		fmt.Println("No changes made.")
		return nil
	}

	// Update fragment content
	frag.Content = newContent
	bundle.Fragments[fragName] = frag

	// Auto-distill if not marked as no_distill
	if !frag.NoDistill {
		fmt.Printf("Distilling %s...", fragName)

		// Load distill prompt
		distillPrompt, err := loadDistillPrompt()
		if err != nil {
			fmt.Printf(" skipped (no distill prompt)\n")
		} else {
			// Get plugin config
			pluginName := fullCfg.GetDefaultLLMPlugin()
			pluginCfg := fullCfg.LM.Plugins[pluginName]

			// Build sibling context
			siblingCtx := buildSiblingContext(bundle, "fragments/"+fragName)

			distilled, modelID, err := distillWithModel(pluginName, pluginCfg.Env, fragName, frag.Content, distillPrompt, siblingCtx)
			if err != nil {
				fmt.Printf(" failed: %v\n", err)
			} else {
				frag.Distilled = distilled
				frag.DistilledBy = modelID
				frag.ContentHash = frag.ComputeContentHash()
				bundle.Fragments[fragName] = frag
				fmt.Printf(" done\n")
			}
		}
	}

	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf("Updated fragment %q in bundle %q\n", fragName, bundle.Name)
	printPushReminder(bundle)
	return nil
}

func editPrompt(cfg interface{ GetDefaultLLMPlugin() string }, bundle *bundles.Bundle, promptName string) error {
	prompt, exists := bundle.Prompts[promptName]
	if !exists {
		return fmt.Errorf("prompt not found: %s\n\nAvailable prompts: %s", promptName, strings.Join(bundle.PromptNames(), ", "))
	}

	// Get full config for editor and plugin
	fullCfg, err := GetConfig()
	if err != nil {
		return err
	}

	// Edit content using editor
	newContent, err := editInEditor(fullCfg, prompt.Content, promptName+".md")
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	if newContent == prompt.Content {
		fmt.Println("No changes made.")
		return nil
	}

	// Update prompt content
	prompt.Content = newContent
	bundle.Prompts[promptName] = prompt

	// Auto-distill if not marked as no_distill
	if !prompt.NoDistill {
		fmt.Printf("Distilling %s...", promptName)

		// Load distill prompt
		distillPrompt, err := loadDistillPrompt()
		if err != nil {
			fmt.Printf(" skipped (no distill prompt)\n")
		} else {
			// Get plugin config
			pluginName := fullCfg.GetDefaultLLMPlugin()
			pluginCfg := fullCfg.LM.Plugins[pluginName]

			// Build sibling context
			siblingCtx := buildSiblingContext(bundle, "prompts/"+promptName)

			distilled, modelID, err := distillWithModel(pluginName, pluginCfg.Env, promptName, prompt.Content, distillPrompt, siblingCtx)
			if err != nil {
				fmt.Printf(" failed: %v\n", err)
			} else {
				prompt.Distilled = distilled
				prompt.DistilledBy = modelID
				prompt.ContentHash = prompt.ComputeContentHash()
				bundle.Prompts[promptName] = prompt
				fmt.Printf(" done\n")
			}
		}
	}

	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf("Updated prompt %q in bundle %q\n", promptName, bundle.Name)
	printPushReminder(bundle)
	return nil
}

// printPushReminder prints a reminder to push the bundle changes.
func printPushReminder(bundle *bundles.Bundle) {
	// Extract the local bundle name from the path for push command
	bundleName := bundle.Name
	if bundle.Path != "" {
		// Try to get a cleaner reference from the path
		dir := filepath.Dir(bundle.Path)
		base := filepath.Base(bundle.Path)
		base = strings.TrimSuffix(base, ".yaml")
		if strings.Contains(dir, "bundles") {
			// Extract remote/name format from path like .ctxloom/bundles/ctxloom-github/core.yaml
			parts := strings.Split(dir, string(filepath.Separator))
			for i, p := range parts {
				if p == "bundles" && i+1 < len(parts) {
					bundleName = strings.Join(append(parts[i+1:], base), "/")
					break
				}
			}
		}
	}

	fmt.Println()
	fmt.Println("Bundle modified. To publish changes:")
	fmt.Printf("  ctxloom bundle push %s [remote]\n", bundleName)
}

func init() {
	rootCmd.AddCommand(editCmd)
}
