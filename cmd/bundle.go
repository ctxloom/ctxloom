package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/bundles"
	"github.com/benjaminabbitt/scm/internal/config"
	pb "github.com/benjaminabbitt/scm/internal/lm/grpc"
)

var bundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "Manage SCM bundles",
	Long: `Manage SCM bundles - versioned collections of fragments, prompts, and MCP servers.

Bundles are the primary content unit in SCM. They group related context fragments,
prompts, and optional MCP server configurations with a single version.

Examples:
  scm bundle list                  # List all installed bundles
  scm bundle show go-tools         # Show bundle contents
  scm bundle create my-bundle      # Create a new bundle
  scm bundle export go-tools ./out # Export bundle to directory
  scm bundle import ./my-bundle.yaml # Import bundle from file`,
}

var bundleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed bundles",
	Long: `List all bundles installed in the .scm/bundles directory.

Shows bundle name, version, description, and content summary.`,
	RunE: runBundleList,
}

func runBundleList(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		fmt.Println("No bundles directory found. Create one with: mkdir -p .scm/bundles")
		return nil
	}

	loader := bundles.NewLoader(bundleDirs, false)
	bundleInfos, err := loader.List()
	if err != nil {
		return fmt.Errorf("failed to list bundles: %w", err)
	}

	if len(bundleInfos) == 0 {
		fmt.Println("No bundles installed.")
		fmt.Println("Pull bundles with: scm remote pull <remote>/bundle-name --type bundle")
		return nil
	}

	fmt.Printf("Installed bundles (%d):\n\n", len(bundleInfos))
	for _, info := range bundleInfos {
		fmt.Printf("  %s", info.Name)
		if info.Version != "" {
			fmt.Printf(" (v%s)", info.Version)
		}
		fmt.Println()

		if info.Description != "" {
			fmt.Printf("    %s\n", info.Description)
		}

		var parts []string
		if info.FragmentCount > 0 {
			parts = append(parts, fmt.Sprintf("%d fragments", info.FragmentCount))
		}
		if info.PromptCount > 0 {
			parts = append(parts, fmt.Sprintf("%d prompts", info.PromptCount))
		}
		if info.MCPCount > 0 {
			if info.MCPCount == 1 {
				parts = append(parts, "1 MCP server")
			} else {
				parts = append(parts, fmt.Sprintf("%d MCP servers", info.MCPCount))
			}
		}
		if len(parts) > 0 {
			fmt.Printf("    Contains: %s\n", strings.Join(parts, ", "))
		}
		if len(info.Tags) > 0 {
			fmt.Printf("    Tags: %s\n", strings.Join(info.Tags, ", "))
		}
		fmt.Println()
	}

	return nil
}

var bundleShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show bundle contents",
	Long: `Display detailed information about a bundle.

Shows all fragments, prompts, and MCP server configuration contained in the bundle.`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleShow,
}

func runBundleShow(cmd *cobra.Command, args []string) error {
	name := args[0]
	if name == "help" {
		return cmd.Help()
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return fmt.Errorf("no bundles directory found")
	}

	loader := bundles.NewLoader(bundleDirs, false)
	bundle, err := loader.Load(name)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", name)
	}

	// Header
	fmt.Printf("Bundle: %s\n", bundle.Name)
	if bundle.Version != "" {
		fmt.Printf("Version: %s\n", bundle.Version)
	}
	if bundle.Author != "" {
		fmt.Printf("Author: %s\n", bundle.Author)
	}
	if bundle.Description != "" {
		fmt.Printf("Description: %s\n", bundle.Description)
	}
	if len(bundle.Tags) > 0 {
		fmt.Printf("Tags: %s\n", strings.Join(bundle.Tags, ", "))
	}
	fmt.Printf("Path: %s\n", bundle.Path)
	fmt.Println()

	// MCP Servers
	if bundle.HasMCP() {
		fmt.Printf("MCP Servers (%d):\n", bundle.MCPCount())
		for _, name := range bundle.MCPNames() {
			mcp := bundle.MCP[name]
			fmt.Printf("  - %s\n", name)
			fmt.Printf("      Command: %s\n", mcp.Command)
			if len(mcp.Args) > 0 {
				fmt.Printf("      Args: %s\n", strings.Join(mcp.Args, " "))
			}
			if len(mcp.Env) > 0 {
				fmt.Println("      Env:")
				for k, v := range mcp.Env {
					fmt.Printf("        %s=%s\n", k, v)
				}
			}
			if mcp.Notes != "" {
				fmt.Printf("      Notes: %s\n", mcp.Notes)
			}
			if mcp.Installation != "" {
				fmt.Printf("      Installation: %s\n", mcp.Installation)
			}
		}
		fmt.Println()
	}

	// Fragments
	if len(bundle.Fragments) > 0 {
		fmt.Printf("Fragments (%d):\n", len(bundle.Fragments))
		for _, name := range bundle.FragmentNames() {
			frag := bundle.Fragments[name]
			fmt.Printf("  - %s", name)
			if len(frag.Tags) > 0 {
				fmt.Printf(" [%s]", strings.Join(frag.Tags, ", "))
			}
			if frag.Distilled != "" {
				fmt.Printf(" (distilled)")
			} else if frag.NoDistill {
				fmt.Printf(" (no_distill)")
			}
			fmt.Println()
			// Show first line of content
			firstLine := strings.Split(strings.TrimSpace(frag.Content), "\n")[0]
			if len(firstLine) > 70 {
				firstLine = firstLine[:67] + "..."
			}
			fmt.Printf("      %s\n", firstLine)
		}
		fmt.Println()
	}

	// Prompts
	if len(bundle.Prompts) > 0 {
		fmt.Printf("Prompts (%d):\n", len(bundle.Prompts))
		for _, name := range bundle.PromptNames() {
			prompt := bundle.Prompts[name]
			fmt.Printf("  - %s", name)
			if len(prompt.Tags) > 0 {
				fmt.Printf(" [%s]", strings.Join(prompt.Tags, ", "))
			}
			if prompt.Distilled != "" {
				fmt.Printf(" (distilled)")
			} else if prompt.NoDistill {
				fmt.Printf(" (no_distill)")
			}
			fmt.Println()
			if prompt.Description != "" {
				fmt.Printf("      %s\n", prompt.Description)
			}
		}
		fmt.Println()
	}

	// Notes
	if bundle.Notes != "" {
		fmt.Println("Notes:")
		fmt.Printf("  %s\n", bundle.Notes)
	}

	return nil
}

var bundleCreateDesc string

var bundleCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new bundle",
	Long: `Create a new bundle file in .scm/bundles.

Creates a skeleton bundle YAML file that you can edit to add content.`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleCreate,
}

func runBundleCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	if name == "help" {
		return cmd.Help()
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Use first SCM path (project or home)
	bundleDir := filepath.Join(cfg.SCMPaths[0], "bundles")
	if err := os.MkdirAll(bundleDir, 0755); err != nil {
		return fmt.Errorf("failed to create bundles directory: %w", err)
	}

	bundlePath := filepath.Join(bundleDir, name+".yaml")
	if _, err := os.Stat(bundlePath); err == nil {
		return fmt.Errorf("bundle already exists: %s", bundlePath)
	}

	// Create bundle skeleton
	bundle := bundles.Bundle{
		Version:     "1.0.0",
		Description: bundleCreateDesc,
		Tags:        []string{},
		Fragments: map[string]bundles.BundleFragment{
			"example": {
				Tags:    []string{"example"},
				Content: "# Example Fragment\n\nAdd your content here.",
			},
		},
		Prompts: map[string]bundles.BundlePrompt{
			"example": {
				Description: "Example prompt",
				Tags:        []string{"example"},
				Content:     "Example prompt content. Describe what this prompt does.",
			},
		},
	}

	data, err := yaml.Marshal(&bundle)
	if err != nil {
		return fmt.Errorf("failed to marshal bundle: %w", err)
	}

	if err := os.WriteFile(bundlePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write bundle: %w", err)
	}

	fmt.Printf("Created bundle: %s\n", bundlePath)
	fmt.Println("Edit the file to add your fragments and prompts.")

	return nil
}

// bundleEdit flags
var (
	bundleEditDesc           string
	bundleEditVersion        string
	bundleEditAddTags        []string
	bundleEditRemoveTags     []string
	bundleEditAddFragment    []string
	bundleEditRemoveFragment []string
	bundleEditAddPrompt      []string
	bundleEditRemovePrompt   []string
	bundleEditAddMCP         []string
	bundleEditRemoveMCP      []string
)

var bundleEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit a bundle",
	Long: `Edit an existing bundle by adding or removing items.

Examples:
  scm bundle edit my-bundle -d "New description"
  scm bundle edit my-bundle --add-fragment coding-standards
  scm bundle edit my-bundle --remove-prompt old-prompt
  scm bundle edit my-bundle --add-tag golang --add-tag testing
  scm bundle edit my-bundle --add-mcp tree-sitter`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleEdit,
}

func runBundleEdit(cmd *cobra.Command, args []string) error {
	name := args[0]
	if name == "help" {
		return cmd.Help()
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return fmt.Errorf("no bundles directory found")
	}

	loader := bundles.NewLoader(bundleDirs, false)
	bundle, err := loader.Load(name)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", name)
	}

	modified := false

	// Update description
	if bundleEditDesc != "" {
		bundle.Description = bundleEditDesc
		modified = true
	}

	// Update version
	if bundleEditVersion != "" {
		bundle.Version = bundleEditVersion
		modified = true
	}

	// Add tags
	for _, tag := range bundleEditAddTags {
		if !sliceContains(bundle.Tags, tag) {
			bundle.Tags = append(bundle.Tags, tag)
			modified = true
		}
	}

	// Remove tags
	for _, tag := range bundleEditRemoveTags {
		bundle.Tags = sliceRemove(bundle.Tags, tag)
		modified = true
	}

	// Add fragments (creates empty placeholder)
	for _, fragName := range bundleEditAddFragment {
		if bundle.Fragments == nil {
			bundle.Fragments = make(map[string]bundles.BundleFragment)
		}
		if _, exists := bundle.Fragments[fragName]; !exists {
			bundle.Fragments[fragName] = bundles.BundleFragment{
				Content: "# " + fragName + "\n\nAdd content here.",
			}
			modified = true
			fmt.Printf("Added fragment: %s\n", fragName)
		} else {
			fmt.Printf("Fragment already exists: %s\n", fragName)
		}
	}

	// Remove fragments
	for _, fragName := range bundleEditRemoveFragment {
		if bundle.Fragments != nil {
			if _, exists := bundle.Fragments[fragName]; exists {
				delete(bundle.Fragments, fragName)
				modified = true
				fmt.Printf("Removed fragment: %s\n", fragName)
			} else {
				fmt.Printf("Fragment not found: %s\n", fragName)
			}
		}
	}

	// Add prompts (creates empty placeholder)
	for _, promptName := range bundleEditAddPrompt {
		if bundle.Prompts == nil {
			bundle.Prompts = make(map[string]bundles.BundlePrompt)
		}
		if _, exists := bundle.Prompts[promptName]; !exists {
			bundle.Prompts[promptName] = bundles.BundlePrompt{
				Description: promptName,
				Content:     "Add prompt content here.",
			}
			modified = true
			fmt.Printf("Added prompt: %s\n", promptName)
		} else {
			fmt.Printf("Prompt already exists: %s\n", promptName)
		}
	}

	// Remove prompts
	for _, promptName := range bundleEditRemovePrompt {
		if bundle.Prompts != nil {
			if _, exists := bundle.Prompts[promptName]; exists {
				delete(bundle.Prompts, promptName)
				modified = true
				fmt.Printf("Removed prompt: %s\n", promptName)
			} else {
				fmt.Printf("Prompt not found: %s\n", promptName)
			}
		}
	}

	// Add MCP servers (creates placeholder)
	for _, mcpName := range bundleEditAddMCP {
		if bundle.MCP == nil {
			bundle.MCP = make(map[string]bundles.BundleMCP)
		}
		if _, exists := bundle.MCP[mcpName]; !exists {
			bundle.MCP[mcpName] = bundles.BundleMCP{
				Command: mcpName,
			}
			modified = true
			fmt.Printf("Added MCP server: %s\n", mcpName)
		} else {
			fmt.Printf("MCP server already exists: %s\n", mcpName)
		}
	}

	// Remove MCP servers
	for _, mcpName := range bundleEditRemoveMCP {
		if bundle.MCP != nil {
			if _, exists := bundle.MCP[mcpName]; exists {
				delete(bundle.MCP, mcpName)
				modified = true
				fmt.Printf("Removed MCP server: %s\n", mcpName)
			} else {
				fmt.Printf("MCP server not found: %s\n", mcpName)
			}
		}
	}

	if !modified {
		fmt.Println("No changes made. Use flags to specify what to edit.")
		return cmd.Help()
	}

	// Save the bundle
	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf("Updated bundle: %s\n", bundle.Path)
	return nil
}

func sliceContains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func sliceRemove(slice []string, item string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}

var bundleExportCmd = &cobra.Command{
	Use:   "export <name> <dest-dir>",
	Short: "Export a bundle to a directory",
	Long: `Export a bundle from .scm/bundles to an arbitrary directory.

Useful for publishing bundles to a shared repository like scm-github.
The bundle is copied as-is, preserving all content including distilled versions.

Examples:
  scm bundle export go-tools ../scm-github/scm/v1/bundles
  scm bundle export my-bundle ./exports`,
	Args: cobra.ExactArgs(2),
	RunE: runBundleExport,
}

func runBundleExport(cmd *cobra.Command, args []string) error {
	name := args[0]
	destDir := args[1]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return fmt.Errorf("no bundles directory found")
	}

	loader := bundles.NewLoader(bundleDirs, false)
	bundle, err := loader.Load(name)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", name)
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Read source file
	srcData, err := os.ReadFile(bundle.Path)
	if err != nil {
		return fmt.Errorf("failed to read bundle: %w", err)
	}

	// Write to destination
	destPath := filepath.Join(destDir, filepath.Base(bundle.Path))
	if err := os.WriteFile(destPath, srcData, 0644); err != nil {
		return fmt.Errorf("failed to write bundle: %w", err)
	}

	fmt.Printf("Exported: %s -> %s\n", bundle.Path, destPath)
	return nil
}

var bundleImportForce bool

var bundleImportCmd = &cobra.Command{
	Use:   "import <path>",
	Short: "Import a bundle from a local file",
	Long: `Import a bundle from a local YAML file into .scm/bundles.

The bundle is copied into the local .scm/bundles directory.
Use --force to overwrite an existing bundle.

Examples:
  scm bundle import ../scm-github/scm/v1/bundles/go-tools.yaml
  scm bundle import ./my-bundle.yaml --force`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleImport,
}

func runBundleImport(cmd *cobra.Command, args []string) error {
	srcPath := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Verify source exists and is valid
	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("failed to read source file: %w", err)
	}

	// Parse to validate it's a valid bundle
	bundle, err := bundles.ParseBundle(srcData)
	if err != nil {
		return fmt.Errorf("invalid bundle file: %w", err)
	}

	// Determine destination path
	bundleDir := filepath.Join(cfg.SCMPaths[0], "bundles")
	if err := os.MkdirAll(bundleDir, 0755); err != nil {
		return fmt.Errorf("failed to create bundles directory: %w", err)
	}

	destPath := filepath.Join(bundleDir, filepath.Base(srcPath))

	// Check if destination exists
	if _, err := os.Stat(destPath); err == nil && !bundleImportForce {
		return fmt.Errorf("bundle already exists: %s (use --force to overwrite)", destPath)
	}

	// Write to destination
	if err := os.WriteFile(destPath, srcData, 0644); err != nil {
		return fmt.Errorf("failed to write bundle: %w", err)
	}

	fmt.Printf("Imported: %s -> %s\n", srcPath, destPath)
	fmt.Printf("  Version: %s\n", bundle.Version)
	fmt.Printf("  Fragments: %d, Prompts: %d, MCP: %d\n", len(bundle.Fragments), len(bundle.Prompts), len(bundle.MCP))

	return nil
}

var bundleViewCmd = &cobra.Command{
	Use:   "view <name[#path]>",
	Short: "View bundle content",
	Long: `View bundle content, optionally drilling into specific items.

Without a path, displays the full bundle YAML.
With a path after #, displays just that item's content.

Path formats:
  bundle-name                     Full bundle YAML
  bundle-name#fragments/name      Fragment content
  bundle-name#prompts/name        Prompt content
  bundle-name#mcp/name            MCP server config

Examples:
  scm bundle view core-practices
  scm bundle view core-practices#fragments/tdd
  scm bundle view mcp-tasks#prompts/setup-tasks
  scm bundle view sequential-thinking#mcp/default`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleView,
}

var bundleViewDistilled bool

func runBundleView(cmd *cobra.Command, args []string) error {
	ref := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return fmt.Errorf("no bundles directory found")
	}

	loader := bundles.NewLoader(bundleDirs, false)

	// Parse reference: bundle-name or bundle-name#path
	bundleName := ref
	itemPath := ""
	if idx := strings.Index(ref, "#"); idx != -1 {
		bundleName = ref[:idx]
		itemPath = ref[idx+1:]
	}

	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	// If no path, show full bundle YAML
	if itemPath == "" {
		data, err := os.ReadFile(bundle.Path)
		if err != nil {
			return fmt.Errorf("failed to read bundle: %w", err)
		}
		fmt.Print(string(data))
		return nil
	}

	// Parse path: type/name
	parts := strings.SplitN(itemPath, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid path format: %s (expected type/name)", itemPath)
	}
	itemType := parts[0]
	itemName := parts[1]

	switch itemType {
	case "fragments":
		frag, ok := bundle.Fragments[itemName]
		if !ok {
			return fmt.Errorf("fragment not found: %s", itemName)
		}
		content := frag.Content
		if bundleViewDistilled && frag.Distilled != "" {
			content = frag.Distilled
			fmt.Println("# (distilled version)")
		}
		fmt.Print(content)
		if !strings.HasSuffix(content, "\n") {
			fmt.Println()
		}

	case "prompts":
		prompt, ok := bundle.Prompts[itemName]
		if !ok {
			return fmt.Errorf("prompt not found: %s", itemName)
		}
		content := prompt.Content
		if bundleViewDistilled && prompt.Distilled != "" {
			content = prompt.Distilled
			fmt.Println("# (distilled version)")
		}
		fmt.Print(content)
		if !strings.HasSuffix(content, "\n") {
			fmt.Println()
		}

	case "mcp":
		mcp, ok := bundle.MCP[itemName]
		if !ok {
			// Try "default" if single MCP and name doesn't match
			if len(bundle.MCP) == 1 {
				for name, m := range bundle.MCP {
					mcp = m
					itemName = name
					ok = true
					break
				}
			}
			if !ok {
				return fmt.Errorf("mcp server not found: %s", itemName)
			}
		}
		data, err := yaml.Marshal(mcp)
		if err != nil {
			return fmt.Errorf("failed to marshal MCP config: %w", err)
		}
		fmt.Printf("# MCP Server: %s\n", itemName)
		fmt.Print(string(data))

	default:
		return fmt.Errorf("unknown item type: %s (expected fragments, prompts, or mcp)", itemType)
	}

	return nil
}

var bundleDistillForce bool
var bundleDistillDryRun bool
var bundleDistillPlugin string

var bundleDistillCmd = &cobra.Command{
	Use:   "distill <file-pattern>...",
	Short: "Distill bundle files to create token-efficient versions",
	Long: `Distill bundle files to create minimal-token versions that preserve meaning.

This command processes each fragment and prompt in the bundle through an LLM
to create a compressed version. The distilled content, content hash, and
model info are written back to the bundle file.

Supports glob patterns to process multiple files at once.

Examples:
  scm bundle distill ./my-bundle.yaml                    # Single file
  scm bundle distill .scm/bundles/*.yaml                 # All bundles in directory
  scm bundle distill .scm/bundles/**/*.yaml              # Recursive
  scm bundle distill bundle1.yaml bundle2.yaml           # Multiple files
  scm bundle distill ./my-bundle.yaml --force            # Re-distill all items
  scm bundle distill ./my-bundle.yaml --dry-run          # Preview what would be distilled`,
	Args: cobra.MinimumNArgs(1),
	RunE: runBundleDistill,
}

func runBundleDistill(cmd *cobra.Command, args []string) error {
	// Expand glob patterns to get list of files
	var files []string
	for _, pattern := range args {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			// Try as literal path
			if _, err := os.Stat(pattern); err == nil {
				files = append(files, pattern)
			} else {
				fmt.Fprintf(os.Stderr, "Warning: no files match %q\n", pattern)
			}
		} else {
			files = append(files, matches...)
		}
	}

	if len(files) == 0 {
		return fmt.Errorf("no files found matching patterns")
	}

	// Load config for plugin settings
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Determine plugin to use
	pluginName := bundleDistillPlugin
	if pluginName == "" {
		pluginName = cfg.GetDefaultLLMPlugin()
	}

	// Get plugin env config
	pluginCfg := cfg.LM.Plugins[pluginName]

	// Load distill prompt
	distillPrompt, err := loadDistillPrompt()
	if err != nil {
		return err
	}

	// Process each file
	totalFiles := 0
	totalItems := 0
	totalSkipped := 0

	for _, filePath := range files {
		// Read and parse bundle
		data, err := os.ReadFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", filePath, err)
			continue
		}

		bundle, err := bundles.ParseBundle(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing %s: %v\n", filePath, err)
			continue
		}
		bundle.Path = filePath

		fmt.Printf("Processing: %s\n", filePath)
		modified := false

		// Distill each fragment
		for name, frag := range bundle.Fragments {
			if frag.NoDistill {
				fmt.Printf("  Skipping fragment %s (no_distill)\n", name)
				totalSkipped++
				continue
			}

			if !bundleDistillForce && !frag.NeedsDistill() {
				fmt.Printf("  Skipping fragment %s (unchanged)\n", name)
				totalSkipped++
				continue
			}

			if bundleDistillDryRun {
				fmt.Printf("  Would distill fragment: %s\n", name)
				totalItems++
				continue
			}

			fmt.Printf("  Distilling fragment: %s...", name)

			// Build sibling context
			siblingCtx := buildSiblingContext(bundle, "fragments/"+name)

			distilled, modelID, err := distillWithModel(pluginName, pluginCfg.Env, name, frag.Content, distillPrompt, siblingCtx)
			if err != nil {
				fmt.Printf(" FAILED: %v\n", err)
				continue
			}

			frag.Distilled = distilled
			frag.ContentHash = frag.ComputeContentHash()
			frag.DistilledBy = modelID
			bundle.Fragments[name] = frag
			modified = true
			totalItems++

			fmt.Printf(" OK (%s)\n", modelID)
		}

		// Distill each prompt
		for name, prompt := range bundle.Prompts {
			if prompt.NoDistill {
				fmt.Printf("  Skipping prompt %s (no_distill)\n", name)
				totalSkipped++
				continue
			}

			if !bundleDistillForce && !prompt.NeedsDistill() {
				fmt.Printf("  Skipping prompt %s (unchanged)\n", name)
				totalSkipped++
				continue
			}

			if bundleDistillDryRun {
				fmt.Printf("  Would distill prompt: %s\n", name)
				totalItems++
				continue
			}

			fmt.Printf("  Distilling prompt: %s...", name)

			// Build sibling context
			siblingCtx := buildSiblingContext(bundle, "prompts/"+name)

			distilled, modelID, err := distillWithModel(pluginName, pluginCfg.Env, name, prompt.Content, distillPrompt, siblingCtx)
			if err != nil {
				fmt.Printf(" FAILED: %v\n", err)
				continue
			}

			prompt.Distilled = distilled
			prompt.ContentHash = prompt.ComputeContentHash()
			prompt.DistilledBy = modelID
			bundle.Prompts[name] = prompt
			modified = true
			totalItems++

			fmt.Printf(" OK (%s)\n", modelID)
		}

		// Save bundle if modified
		if modified && !bundleDistillDryRun {
			if err := bundle.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "  Error saving %s: %v\n", filePath, err)
				continue
			}
			totalFiles++
		}
	}

	// Summary
	if bundleDistillDryRun {
		fmt.Printf("\nDry run: would distill %d items\n", totalItems)
	} else {
		var parts []string
		if totalItems > 0 {
			parts = append(parts, fmt.Sprintf("distilled %d items in %d files", totalItems, totalFiles))
		}
		if totalSkipped > 0 {
			parts = append(parts, fmt.Sprintf("skipped %d", totalSkipped))
		}
		if len(parts) > 0 {
			fmt.Printf("\n%s\n", strings.Join(parts, ", "))
		} else {
			fmt.Println("\nNo items to distill.")
		}
	}

	return nil
}

// defaultDistillPrompt is used when no distill prompt is found in bundles.
const defaultDistillPrompt = `You are a context compression assistant. Compress the following content while preserving all essential information for an AI coding assistant. Remove redundancy, simplify language, use abbreviations where clear, and maintain technical accuracy. Output only the compressed content.`

// loadDistillPrompt loads the distillation prompt from bundles.
func loadDistillPrompt() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return defaultDistillPrompt, nil
	}

	// Try to load "distill" prompt from bundles
	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	prompt, err := loader.GetPrompt("distill")
	if err == nil && prompt.Content != "" {
		return strings.TrimSpace(prompt.Content), nil
	}

	// Use default prompt
	return defaultDistillPrompt, nil
}

// buildSiblingContext creates context about sibling items in a bundle.
func buildSiblingContext(bundle *bundles.Bundle, excludeName string) string {
	var ctx strings.Builder

	ctx.WriteString(fmt.Sprintf("Bundle: %s", bundle.Description))
	if bundle.Version != "" {
		ctx.WriteString(fmt.Sprintf(" (v%s)", bundle.Version))
	}
	ctx.WriteString("\n")

	if len(bundle.Tags) > 0 {
		ctx.WriteString("Tags: ")
		ctx.WriteString(strings.Join(bundle.Tags, ", "))
		ctx.WriteString("\n")
	}
	ctx.WriteString("\n")

	// List sibling fragments
	if len(bundle.Fragments) > 1 || (len(bundle.Fragments) == 1 && !strings.HasPrefix(excludeName, "fragments/")) {
		ctx.WriteString("Sibling fragments:\n")
		for name, frag := range bundle.Fragments {
			if "fragments/"+name == excludeName {
				continue
			}
			firstLine := strings.Split(strings.TrimSpace(frag.Content), "\n")[0]
			if len(firstLine) > 60 {
				firstLine = firstLine[:57] + "..."
			}
			ctx.WriteString(fmt.Sprintf("- %s: %s\n", name, firstLine))
		}
		ctx.WriteString("\n")
	}

	// List sibling prompts
	if len(bundle.Prompts) > 1 || (len(bundle.Prompts) == 1 && !strings.HasPrefix(excludeName, "prompts/")) {
		ctx.WriteString("Sibling prompts:\n")
		for name, prompt := range bundle.Prompts {
			if "prompts/"+name == excludeName {
				continue
			}
			desc := prompt.Description
			if desc == "" {
				desc = strings.Split(strings.TrimSpace(prompt.Content), "\n")[0]
				if len(desc) > 60 {
					desc = desc[:57] + "..."
				}
			}
			ctx.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
		}
	}

	return ctx.String()
}

// distillWithModel sends content through the LLM and returns distilled content and model ID.
func distillWithModel(pluginName string, env map[string]string, name, content, distillPrompt, siblingCtx string) (string, string, error) {
	// Build content to distill
	var builder strings.Builder

	if siblingCtx != "" {
		builder.WriteString("<bundle_context>\n")
		builder.WriteString(siblingCtx)
		builder.WriteString("\n</bundle_context>\n\n")
		builder.WriteString("CONTEXT-AWARE COMPRESSION:\n")
		builder.WriteString("- The bundle_context shows sibling items that will be loaded together\n")
		builder.WriteString("- DO NOT repeat concepts already covered by siblings - reference them instead\n")
		builder.WriteString("- Compress knowing this content will be loaded alongside those siblings\n\n")
	}

	builder.WriteString("<content_to_compress>\n# ")
	builder.WriteString(name)
	builder.WriteString("\n\n")
	builder.WriteString(content)
	builder.WriteString("\n</content_to_compress>")

	// Create plugin client
	client, err := pb.NewSelfInvokingClient(pluginName, 0)
	if err != nil {
		return "", "", fmt.Errorf("failed to start plugin: %w", err)
	}
	defer client.Kill()

	// Build request
	req := &pb.RunRequest{
		Prompt: &pb.Fragment{
			Content: builder.String(),
		},
		Fragments: []*pb.Fragment{
			{Content: distillPrompt},
		},
		Options: &pb.RunOptions{
			AutoApprove: true,
			Mode:        pb.ExecutionMode_ONESHOT,
			Env:         env,
		},
	}

	// Execute and capture model info
	var stdout, stderr bytes.Buffer
	result, err := client.RunWithModelInfo(context.Background(), req, &stdout, &stderr)
	if err != nil {
		return "", "", err
	}

	if result.ExitCode != 0 {
		return "", "", fmt.Errorf("LLM exited with code %d: %s", result.ExitCode, stderr.String())
	}

	// Build model ID from model info
	modelID := pluginName
	if result.ModelInfo != nil {
		if result.ModelInfo.ModelName != "" {
			modelID = result.ModelInfo.ModelName
		}
		if result.ModelInfo.ModelVersion != "" {
			modelID = fmt.Sprintf("%s:%s", modelID, result.ModelInfo.ModelVersion)
		}
	}

	// Clean up distilled content
	distilled := cleanDistilledOutput(strings.TrimSpace(stdout.String()))

	return distilled, modelID, nil
}

// preambleRe matches markdown horizontal rules/separators
var preambleRe = regexp.MustCompile(`(?m)^-{3,}\s*$`)

// codeFenceRe matches opening code fences
var codeFenceRe = regexp.MustCompile("^```[a-z]*\\s*\n")

// conversationalStarts are patterns LLMs add despite instructions
var conversationalStarts = []string{
	"here's ", "here is ", "below is ", "below you'll find ",
	"the compressed version", "the following ",
	"i've compressed ", "i have compressed ", "my compressed version",
}

// cleanDistilledOutput removes LLM preamble artifacts.
func cleanDistilledOutput(content string) string {
	content = strings.TrimSpace(content)
	foundPreamble := false

	// Check for conversational prefixes
	lower := strings.ToLower(content)
	for _, prefix := range conversationalStarts {
		if strings.HasPrefix(lower, prefix) {
			foundPreamble = true
			if idx := strings.Index(content, "\n"); idx != -1 {
				content = strings.TrimSpace(content[idx+1:])
			}
			break
		}
	}

	// Strip separator if preamble was found
	if foundPreamble {
		if loc := preambleRe.FindStringIndex(content); loc != nil && loc[0] < 100 {
			after := content[loc[1]:]
			if len(after) > 0 && after[0] == '\n' {
				after = after[1:]
			}
			content = strings.TrimSpace(after)
		}
	}

	// Strip code fence if present
	if loc := codeFenceRe.FindStringIndex(content); loc != nil && loc[0] == 0 {
		content = content[loc[1]:]
		if idx := strings.LastIndex(content, "```"); idx != -1 {
			if strings.TrimSpace(content[idx+3:]) == "" {
				content = strings.TrimSpace(content[:idx])
			}
		}
	}

	return content
}

// ============ Bundle Fragment Commands ============

var bundleFragmentCmd = &cobra.Command{
	Use:   "fragment",
	Short: "Manage fragments within a bundle",
	Long:  `Commands for managing fragments within a bundle.`,
}

var bundleFragmentEditCmd = &cobra.Command{
	Use:   "edit <bundle-name> <fragment-name>",
	Short: "Edit a fragment's content",
	Long: `Edit a fragment's content using your configured editor.

Opens the fragment content in your editor. When you save and close,
the bundle is updated with the new content.

Examples:
  scm bundle fragment edit my-bundle coding-standards
  scm bundle fragment edit go-tools golang`,
	Args: cobra.ExactArgs(2),
	RunE: runBundleFragmentEdit,
}

func runBundleFragmentEdit(cmd *cobra.Command, args []string) error {
	bundleName, fragName := args[0], args[1]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	frag, exists := bundle.Fragments[fragName]
	if !exists {
		return fmt.Errorf("fragment not found: %s", fragName)
	}

	// Edit content using editor
	newContent, err := editInEditor(cfg, frag.Content, fragName+".md")
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
			pluginName := cfg.GetDefaultLLMPlugin()
			pluginCfg := cfg.LM.Plugins[pluginName]

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

	fmt.Printf("Updated fragment %q in bundle %q\n", fragName, bundleName)
	return nil
}

// ============ Bundle Prompt Commands ============

var bundlePromptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Manage prompts within a bundle",
	Long:  `Commands for managing prompts within a bundle.`,
}

var bundlePromptEditCmd = &cobra.Command{
	Use:   "edit <bundle-name> <prompt-name>",
	Short: "Edit a prompt's content",
	Long: `Edit a prompt's content using your configured editor.

Opens the prompt content in your editor. When you save and close,
the bundle is updated with the new content.

Examples:
  scm bundle prompt edit my-bundle code-review
  scm bundle prompt edit go-tools refactor`,
	Args: cobra.ExactArgs(2),
	RunE: runBundlePromptEdit,
}

func runBundlePromptEdit(cmd *cobra.Command, args []string) error {
	bundleName, promptName := args[0], args[1]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	prompt, exists := bundle.Prompts[promptName]
	if !exists {
		return fmt.Errorf("prompt not found: %s", promptName)
	}

	// Edit content using editor
	newContent, err := editInEditor(cfg, prompt.Content, promptName+".md")
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
			pluginName := cfg.GetDefaultLLMPlugin()
			pluginCfg := cfg.LM.Plugins[pluginName]

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

	fmt.Printf("Updated prompt %q in bundle %q\n", promptName, bundleName)
	return nil
}

// ============ Bundle MCP Commands ============

var bundleMCPCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage MCP servers within a bundle",
	Long:  `Commands for managing MCP server configurations within a bundle.`,
}

var bundleMCPEditCmd = &cobra.Command{
	Use:   "edit <bundle-name> <mcp-name>",
	Short: "Edit an MCP server configuration",
	Long: `Edit an MCP server's configuration using your configured editor.

Opens the MCP server config as YAML in your editor. When you save and close,
the bundle is updated with the new configuration.

Examples:
  scm bundle mcp edit my-bundle tree-sitter
  scm bundle mcp edit tools sequential-thinking`,
	Args: cobra.ExactArgs(2),
	RunE: runBundleMCPEdit,
}

func runBundleMCPEdit(cmd *cobra.Command, args []string) error {
	bundleName, mcpName := args[0], args[1]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	mcp, exists := bundle.MCP[mcpName]
	if !exists {
		return fmt.Errorf("MCP server not found: %s", mcpName)
	}

	// Serialize MCP config to YAML for editing
	mcpYAML, err := yaml.Marshal(&mcp)
	if err != nil {
		return fmt.Errorf("failed to serialize MCP config: %w", err)
	}

	// Edit content using editor
	newContent, err := editInEditor(cfg, string(mcpYAML), mcpName+".yaml")
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	if newContent == string(mcpYAML) {
		fmt.Println("No changes made.")
		return nil
	}

	// Parse new YAML
	var newMCP bundles.BundleMCP
	if err := yaml.Unmarshal([]byte(newContent), &newMCP); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}

	bundle.MCP[mcpName] = newMCP

	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf("Updated MCP server %q in bundle %q\n", mcpName, bundleName)
	return nil
}

// editInEditor opens content in the configured editor and returns the edited content.
func editInEditor(cfg *config.Config, content, filename string) (string, error) {
	// Create temp file
	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, filename)
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile)

	// Get editor command
	editor := cfg.Editor.Command
	if editor == "" {
		editor = os.Getenv("EDITOR")
		if editor == "" {
			editor = "nano"
		}
	}

	// Build command
	args := append(cfg.Editor.Args, tmpFile)

	// Run editor
	editorCmd := exec.Command(editor, args...)
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr

	if err := editorCmd.Run(); err != nil {
		return "", fmt.Errorf("editor exited with error: %w", err)
	}

	// Read back content
	newContent, err := os.ReadFile(tmpFile)
	if err != nil {
		return "", fmt.Errorf("failed to read edited file: %w", err)
	}

	return string(newContent), nil
}

func init() {
	rootCmd.AddCommand(bundleCmd)
	bundleCmd.AddCommand(bundleListCmd)
	bundleCmd.AddCommand(bundleShowCmd)
	bundleCmd.AddCommand(bundleViewCmd)
	bundleCmd.AddCommand(bundleCreateCmd)
	bundleCmd.AddCommand(bundleEditCmd)
	bundleCmd.AddCommand(bundleExportCmd)
	bundleCmd.AddCommand(bundleImportCmd)
	bundleCmd.AddCommand(bundleDistillCmd)

	// Fragment subcommands
	bundleCmd.AddCommand(bundleFragmentCmd)
	bundleFragmentCmd.AddCommand(bundleFragmentEditCmd)

	// Prompt subcommands
	bundleCmd.AddCommand(bundlePromptCmd)
	bundlePromptCmd.AddCommand(bundlePromptEditCmd)

	// MCP subcommands
	bundleCmd.AddCommand(bundleMCPCmd)
	bundleMCPCmd.AddCommand(bundleMCPEditCmd)

	bundleCreateCmd.Flags().StringVarP(&bundleCreateDesc, "description", "d", "", "Bundle description")
	bundleImportCmd.Flags().BoolVarP(&bundleImportForce, "force", "f", false, "Overwrite existing bundle")
	bundleViewCmd.Flags().BoolVarP(&bundleViewDistilled, "distilled", "d", false, "Show distilled version if available")

	// bundleEditCmd flags
	bundleEditCmd.Flags().StringVarP(&bundleEditDesc, "description", "d", "", "New description")
	bundleEditCmd.Flags().StringVar(&bundleEditVersion, "version", "", "New version")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddTags, "add-tag", nil, "Tag(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemoveTags, "remove-tag", nil, "Tag(s) to remove")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddFragment, "add-fragment", nil, "Fragment(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemoveFragment, "remove-fragment", nil, "Fragment(s) to remove")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddPrompt, "add-prompt", nil, "Prompt(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemovePrompt, "remove-prompt", nil, "Prompt(s) to remove")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddMCP, "add-mcp", nil, "MCP server(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemoveMCP, "remove-mcp", nil, "MCP server(s) to remove")

	bundleDistillCmd.Flags().BoolVarP(&bundleDistillForce, "force", "f", false, "Re-distill even if unchanged")
	bundleDistillCmd.Flags().BoolVarP(&bundleDistillDryRun, "dry-run", "n", false, "Preview what would be distilled")
	bundleDistillCmd.Flags().StringVarP(&bundleDistillPlugin, "plugin", "l", "", "LLM to use (default from config)")
}
