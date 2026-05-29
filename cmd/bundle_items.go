package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/iox"
)

// ============ Bundle Fragment Commands ============

var bundleFragmentCmd = &cobra.Command{
	Use:   "fragment",
	Short: "Manage fragments within a bundle",
	Long:  `Commands for managing fragments within a bundle.`,
}

var bundleFragmentListCmd = &cobra.Command{
	Use:   "list <bundle-name>",
	Short: "List fragments in a bundle",
	Long: `List all fragments in a specific bundle.

Examples:
  ctxloom bundle fragment list my-bundle
  ctxloom bundle fragment list go-tools`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleFragmentList,
}

func runBundleFragmentList(cmd *cobra.Command, args []string) error {
	bundleName := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	return renderBundleFragmentList(cmd.OutOrStdout(), bundle)
}

// renderBundleFragmentList writes one line per fragment to out:
// `<name>[ [tag1, tag2]]`. Empty bundle gets a helpful one-line hint.
// Iteration order follows the map iteration order (intentionally
// unordered — the show command's FragmentNames() produces a stable
// sort instead).
func renderBundleFragmentList(out io.Writer, bundle *bundles.Bundle) error {
	w := iox.NewErrWriter(out)
	if len(bundle.Fragments) == 0 {
		w.Println("No fragments in this bundle.")
		return w.Err()
	}
	for name, frag := range bundle.Fragments {
		w.Printf("%s", name)
		if len(frag.Tags) > 0 {
			w.Printf(" [%s]", strings.Join(frag.Tags, ", "))
		}
		w.Println()
	}
	return w.Err()
}

var bundleFragmentEditCmd = &cobra.Command{
	Use:   "edit <bundle-name> <fragment-name>",
	Short: "Edit a fragment's content",
	Long: `Edit a fragment's content using your configured editor.

Opens the fragment content in your editor. When you save and close,
the bundle is updated with the new content.

Examples:
  ctxloom bundle fragment edit my-bundle coding-standards
  ctxloom bundle fragment edit go-tools golang`,
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

var bundlePromptListCmd = &cobra.Command{
	Use:   "list <bundle-name>",
	Short: "List prompts in a bundle",
	Long: `List all prompts in a specific bundle.

Examples:
  ctxloom bundle prompt list my-bundle
  ctxloom bundle prompt list go-tools`,
	Args: cobra.ExactArgs(1),
	RunE: runBundlePromptList,
}

func runBundlePromptList(cmd *cobra.Command, args []string) error {
	bundleName := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	return renderBundlePromptList(cmd.OutOrStdout(), bundle)
}

// renderBundlePromptList writes one line per prompt name to out, or a
// one-line hint when the bundle has no prompts. Names only — tag and
// description rendering live in renderBundlePromptEntry (used by show).
func renderBundlePromptList(out io.Writer, bundle *bundles.Bundle) error {
	w := iox.NewErrWriter(out)
	if len(bundle.Prompts) == 0 {
		w.Println("No prompts in this bundle.")
		return w.Err()
	}
	for name := range bundle.Prompts {
		w.Println(name)
	}
	return w.Err()
}

var bundlePromptEditCmd = &cobra.Command{
	Use:   "edit <bundle-name> <prompt-name>",
	Short: "Edit a prompt's content",
	Long: `Edit a prompt's content using your configured editor.

Opens the prompt content in your editor. When you save and close,
the bundle is updated with the new content.

Examples:
  ctxloom bundle prompt edit my-bundle code-review
  ctxloom bundle prompt edit go-tools refactor`,
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
  ctxloom bundle mcp edit my-bundle tree-sitter
  ctxloom bundle mcp edit tools sequential-thinking`,
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
	defer func() { _ = os.Remove(tmpFile) }()

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
