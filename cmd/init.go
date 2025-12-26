package cmd

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"mlcm/internal/config"
	"mlcm/internal/fsys"
	"mlcm/internal/gitutil"
	"mlcm/internal/schema"
	"mlcm/resources"
)

var skipFragments string
var fromGitRepo string
var verboseInit bool

// findGitRoot returns the root of the git repository by walking up looking for .git.
// Returns empty string if not in a git repo.
func findGitRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	root, err := gitutil.FindRoot(dir)
	if err != nil {
		return ""
	}
	return root
}

// printCopyResult displays a copy result with optional file listing.
func printCopyResult(result *resources.CopyResult, source string, verbose bool) {
	if result == nil || result.Total() == 0 {
		return
	}

	fmt.Printf("  From %s:\n", source)
	if len(result.Added) > 0 {
		fmt.Printf("    + Added:     %d files\n", len(result.Added))
		if verbose {
			for _, f := range result.Added {
				fmt.Printf("        %s\n", f)
			}
		}
	}
	if len(result.Updated) > 0 {
		fmt.Printf("    ~ Updated:   %d files\n", len(result.Updated))
		if verbose {
			for _, f := range result.Updated {
				fmt.Printf("        %s\n", f)
			}
		}
	}
	if len(result.Unchanged) > 0 && verbose {
		fmt.Printf("    = Unchanged: %d files\n", len(result.Unchanged))
	}
}

var initCmd = &cobra.Command{
	Use:   "init [personas...]",
	Short: "Initialize .mlcm in current directory",
	Long: `Initialize a .mlcm directory in the current directory with:
  - context-fragments/  (for context fragment files)
  - prompts/            (for prompt templates)
  - config.yaml         (for configuration)

Persona filtering:
  If personas are specified, only fragments associated with those personas
  are copied. Without personas, all fragments are copied.

  Example: mlcm init go-developer reviewer

Fragment sources (in order, later overwrites earlier):
  1. Embedded default fragments
  2. ~/.mlcm/context-fragments/ (your personal fragments)
  3. Git repository (if --from-git is specified)

Use --skip-fragments to control which sources are skipped:
  --skip-fragments          Skip embedded fragments (default)
  --skip-fragments=local    Skip ~/.mlcm fragments only
  --skip-fragments=both     Skip all fragment copying

Use --from-git to clone fragments from a remote git repository:
  --from-git=https://github.com/user/fragments-repo`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Try to find git root, fallback to pwd
		rootDir := findGitRoot()
		if rootDir == "" {
			var err error
			rootDir, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}
		}
		mlcmDir := filepath.Join(rootDir, config.MLCMDirName)
		return initMLCMDirectory(mlcmDir, args)
	},
}

var initHomeCmd = &cobra.Command{
	Use:   "home [personas...]",
	Short: "Initialize .mlcm in home directory",
	Long: `Initialize a .mlcm directory in your home directory (~/.mlcm) with:
  - context-fragments/  (for personal/shared fragment files)
  - prompts/            (for personal prompt templates)
  - config.yaml         (for personal configuration and persona definitions)

This serves as a template source for project initialization.

Persona filtering:
  If personas are specified, only fragments associated with those personas
  are copied from embedded resources. Without personas, all fragments are copied.

  Example: mlcm init home go-developer`,
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		mlcmDir := filepath.Join(home, config.MLCMDirName)
		return initHomeDirectory(mlcmDir, args)
	},
}

// initHomeDirectory initializes ~/.mlcm with embedded resources only.
func initHomeDirectory(mlcmDir string, personas []string) error {
	// Check if already exists
	if info, err := os.Stat(mlcmDir); err == nil && info.IsDir() {
		fmt.Printf("~/.mlcm already exists at %s\n", mlcmDir)
		fmt.Println("Use 'mlcm init' in a project directory to initialize from home.")
		return nil
	}

	fmt.Printf("Initializing ~/.mlcm at %s\n", mlcmDir)

	// Create main directory
	if err := os.MkdirAll(mlcmDir, 0755); err != nil {
		return fmt.Errorf("failed to create .mlcm directory: %w", err)
	}

	// Create subdirectories
	fragmentsDir := filepath.Join(mlcmDir, config.ContextFragmentsDir)
	if err := os.MkdirAll(fragmentsDir, 0755); err != nil {
		return fmt.Errorf("failed to create context-fragments directory: %w", err)
	}

	promptsDir := filepath.Join(mlcmDir, config.PromptsDir)
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		return fmt.Errorf("failed to create prompts directory: %w", err)
	}

	// Load embedded config to get persona definitions
	embeddedCfg, err := config.LoadEmbeddedConfig()
	if err != nil {
		return fmt.Errorf("failed to load embedded config: %w", err)
	}

	// Track totals
	totalResult := &resources.CopyResult{}
	promptResult := &resources.CopyResult{}

	if len(personas) > 0 {
		fmt.Printf("Filtering for personas: %s\n", strings.Join(personas, ", "))
	}

	fmt.Println("\nContext fragments:")

	// Copy fragments (filtered by persona if specified) - no header for home
	var fragmentFilter []string
	if len(personas) > 0 {
		var err error
		fragmentFilter, err = config.CollectFragmentsForPersonas(embeddedCfg.Personas, personas)
		if err != nil {
			return err
		}
	}
	result, err := resources.CopyFragments(fragmentsDir, fragmentFilter, "")
	if err != nil {
		return fmt.Errorf("failed to copy embedded fragments: %w", err)
	}
	printCopyResult(result, "embedded", verboseInit)
	totalResult.Merge(result)

	// Always copy mlcm-tagged content (internal prompts/fragments) - no header for home
	mlcmFragResult, err := resources.CopyTaggedFragments(fragmentsDir, "mlcm", "")
	if err != nil {
		return fmt.Errorf("failed to copy mlcm fragments: %w", err)
	}
	totalResult.Merge(mlcmFragResult)

	mlcmPromptResult, err := resources.CopyTaggedPrompts(promptsDir, "mlcm", "")
	if err != nil {
		return fmt.Errorf("failed to copy mlcm prompts: %w", err)
	}
	promptResult.Merge(mlcmPromptResult)

	// Print prompts summary if any
	if promptResult.Total() > 0 {
		fmt.Println("\nPrompts:")
		printCopyResult(promptResult, "embedded", verboseInit)
	}

	// Create config file with personas and generators
	configPath := filepath.Join(mlcmDir, config.ConfigFileName+".yaml")
	if err := writeHomeConfig(configPath, embeddedCfg, personas); err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}

	// Summary
	fmt.Println()
	fmt.Printf("MLCM home initialized: %d files created\n",
		len(totalResult.Added)+len(promptResult.Added))
	fmt.Printf("  %s\n", mlcmDir)

	return nil
}

// writeHomeConfig writes the config.yaml for home directory initialization.
func writeHomeConfig(configPath string, embeddedCfg *config.Config, personas []string) error {
	cfgData := make(map[string]interface{})

	// AI config
	cfgData["ai"] = embeddedCfg.AI

	// Generators - include all or filter based on personas
	if len(personas) > 0 {
		genNames := config.CollectGeneratorsForPersonas(embeddedCfg.Personas, personas)
		if len(genNames) > 0 {
			cfgData["generators"] = config.FilterGenerators(embeddedCfg.Generators, genNames)
		}
		cfgData["personas"] = config.FilterPersonas(embeddedCfg.Personas, personas)
	} else {
		if len(embeddedCfg.Generators) > 0 {
			cfgData["generators"] = embeddedCfg.Generators
		}
		if len(embeddedCfg.Personas) > 0 {
			cfgData["personas"] = embeddedCfg.Personas
		}
	}

	data, err := yaml.Marshal(cfgData)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

func initMLCMDirectory(mlcmDir string, personas []string) error {
	// Check if already exists
	isUpdate := false
	if info, err := os.Stat(mlcmDir); err == nil && info.IsDir() {
		isUpdate = true
		fmt.Printf("Updating .mlcm directory at %s\n", mlcmDir)
	} else {
		fmt.Printf("Initializing .mlcm directory at %s\n", mlcmDir)
	}

	// Create main directory
	if err := os.MkdirAll(mlcmDir, 0755); err != nil {
		return fmt.Errorf("failed to create .mlcm directory: %w", err)
	}

	// Create subdirectories
	fragmentsDir := filepath.Join(mlcmDir, config.ContextFragmentsDir)
	if err := os.MkdirAll(fragmentsDir, 0755); err != nil {
		return fmt.Errorf("failed to create context-fragments directory: %w", err)
	}

	promptsDir := filepath.Join(mlcmDir, config.PromptsDir)
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		return fmt.Errorf("failed to create prompts directory: %w", err)
	}

	// Load persona definitions from embedded and home configs
	embeddedCfg, err := config.LoadEmbeddedConfig()
	if err != nil {
		return fmt.Errorf("failed to load embedded config: %w", err)
	}

	homeCfg, err := config.LoadHomeConfig()
	if err != nil {
		return fmt.Errorf("failed to load home config: %w", err)
	}

	// Merge persona definitions (home overrides embedded)
	allPersonas := make(map[string]config.Persona)
	config.MergePersonas(allPersonas, embeddedCfg.Personas)
	if homeCfg != nil {
		config.MergePersonas(allPersonas, homeCfg.Personas)
	}

	allGenerators := make(map[string]config.Generator)
	config.MergeGenerators(allGenerators, embeddedCfg.Generators)
	if homeCfg != nil {
		config.MergeGenerators(allGenerators, homeCfg.Generators)
	}

	// Determine which fragments to copy
	var fragmentFilter []string
	if len(personas) > 0 {
		fragmentFilter, err = config.CollectFragmentsForPersonas(allPersonas, personas)
		if err != nil {
			return err
		}
		fmt.Printf("Filtering for personas: %s\n", strings.Join(personas, ", "))
	}

	// Copy fragments based on --skip-fragments setting
	skipEmbedded := skipFragments == "embedded" || skipFragments == "both"
	skipLocal := skipFragments == "local" || skipFragments == "both"

	// Track totals
	totalResult := &resources.CopyResult{}

	fmt.Println("\nContext fragments:")

	// First, copy embedded fragments
	if !skipEmbedded {
		result, err := resources.CopyFragments(fragmentsDir, fragmentFilter, resources.ProjectHeader)
		if err != nil {
			return fmt.Errorf("failed to copy embedded fragments: %w", err)
		}
		printCopyResult(result, "embedded", verboseInit)
		totalResult.Merge(result)
	}

	// Then, copy from ~/.mlcm (overwrites duplicates)
	if !skipLocal {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		homeFragments := filepath.Join(home, config.MLCMDirName, config.ContextFragmentsDir)
		if info, err := os.Stat(homeFragments); err == nil && info.IsDir() {
			result, err := copyDir(homeFragments, fragmentsDir, fragmentFilter, resources.ProjectHeader)
			if err != nil {
				return fmt.Errorf("failed to copy fragments from %s: %w", homeFragments, err)
			}
			printCopyResult(result, "~/.mlcm", verboseInit)
			totalResult.Merge(result)
		}
	}

	// Copy from git repository if specified (overwrites duplicates)
	if fromGitRepo != "" {
		result, err := copyFromGitRepo(fromGitRepo, fragmentsDir, fragmentFilter, resources.ProjectHeader)
		if err != nil {
			return fmt.Errorf("failed to copy from git repo: %w", err)
		}
		printCopyResult(result, fromGitRepo, verboseInit)
		totalResult.Merge(result)
	}

	// Copy prompts from ~/.mlcm/prompts/ (if they exist)
	promptResult := &resources.CopyResult{}
	if !skipLocal {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		homePrompts := filepath.Join(home, config.MLCMDirName, config.PromptsDir)
		if info, err := os.Stat(homePrompts); err == nil && info.IsDir() {
			result, err := copyDir(homePrompts, promptsDir, nil, resources.ProjectHeader)
			if err != nil {
				return fmt.Errorf("failed to copy prompts from %s: %w", homePrompts, err)
			}
			promptResult.Merge(result)
		}
	}

	// Always copy mlcm-tagged content (internal prompts/fragments) - with header for project
	mlcmFragResult, err := resources.CopyTaggedFragments(fragmentsDir, "mlcm", resources.ProjectHeader)
	if err != nil {
		return fmt.Errorf("failed to copy mlcm fragments: %w", err)
	}
	totalResult.Merge(mlcmFragResult)

	mlcmPromptResult, err := resources.CopyTaggedPrompts(promptsDir, "mlcm", resources.ProjectHeader)
	if err != nil {
		return fmt.Errorf("failed to copy mlcm prompts: %w", err)
	}
	promptResult.Merge(mlcmPromptResult)

	// Print prompts summary if any
	if promptResult.Total() > 0 {
		fmt.Println("\nPrompts:")
		printCopyResult(promptResult, "~/.mlcm + embedded", verboseInit)
	}

	// Create config file
	configPath := filepath.Join(mlcmDir, config.ConfigFileName+".yaml")
	if err := writeProjectConfig(configPath, embeddedCfg, allPersonas, allGenerators, personas); err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}

	// Create .gitkeep for prompts directory if empty
	promptFiles, _ := os.ReadDir(promptsDir)
	if len(promptFiles) == 0 {
		gitkeepPrompts := filepath.Join(promptsDir, ".gitkeep")
		if err := os.WriteFile(gitkeepPrompts, []byte(""), 0644); err != nil {
			return fmt.Errorf("failed to create .gitkeep: %w", err)
		}
	}

	// Summary
	fmt.Println()
	if isUpdate {
		fmt.Printf("MLCM updated: %d added, %d updated, %d unchanged\n",
			len(totalResult.Added)+len(promptResult.Added),
			len(totalResult.Updated)+len(promptResult.Updated),
			len(totalResult.Unchanged)+len(promptResult.Unchanged))
	} else {
		fmt.Printf("MLCM initialized: %d files created\n",
			len(totalResult.Added)+len(promptResult.Added))
	}
	fmt.Printf("  %s\n", mlcmDir)

	return nil
}

// writeProjectConfig writes the config.yaml for project initialization.
func writeProjectConfig(configPath string, embeddedCfg *config.Config, allPersonas map[string]config.Persona, allGenerators map[string]config.Generator, personas []string) error {
	cfgData := make(map[string]interface{})

	// AI config
	cfgData["ai"] = embeddedCfg.AI

	// Include personas and generators (filtered if personas specified)
	if len(personas) > 0 {
		genNames := config.CollectGeneratorsForPersonas(allPersonas, personas)
		if len(genNames) > 0 {
			cfgData["generators"] = config.FilterGenerators(allGenerators, genNames)
		}
		cfgData["personas"] = config.FilterPersonas(allPersonas, personas)
	} else {
		if len(allGenerators) > 0 {
			cfgData["generators"] = allGenerators
		}
		if len(allPersonas) > 0 {
			cfgData["personas"] = allPersonas
		}
	}

	data, err := yaml.Marshal(cfgData)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

// copyDir copies YAML fragment files from src to dst.
//
// Fragments are copied to the project directory to ensure all developers
// working on the project use the same context - providing reproducibility.
//
// Files are validated against the fragment JSON schema before copying.
// Invalid files are skipped with a warning.
//
// If filter is non-nil, only fragments matching the filter are copied.
// Filter entries are paths like "style/direct" (without extension).
// If header is non-empty, it is prepended to YAML files.
func copyDir(src, dst string, filter []string, header string) (*resources.CopyResult, error) {
	result := &resources.CopyResult{}

	// Create validator for schema checking
	validator, err := schema.NewValidator()
	if err != nil {
		return nil, fmt.Errorf("failed to create validator: %w", err)
	}

	// Build filter set if provided
	var allowed map[string]bool
	var found map[string]bool
	if len(filter) > 0 {
		allowed = make(map[string]bool)
		found = make(map[string]bool)
		for _, frag := range filter {
			frag = strings.TrimSuffix(frag, ".yaml")
			frag = strings.TrimSuffix(frag, ".yml")
			allowed[frag] = true
		}
	}

	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		if d.IsDir() {
			if allowed == nil {
				return os.MkdirAll(filepath.Join(dst, relPath), 0755)
			}
			return nil // Directories created as needed when filtering
		}

		name := d.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		// Apply filter if set
		if allowed != nil {
			baseName := strings.TrimSuffix(relPath, ".yaml")
			baseName = strings.TrimSuffix(baseName, ".yml")
			if !allowed[baseName] {
				return nil
			}
			found[baseName] = true
		}

		// Validate against schema before copying
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot read %s: %v\n", path, err)
			return nil
		}
		if err := validator.ValidateBytes(data); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid fragment %s: %v\n", path, err)
			return nil
		}

		dstPath := filepath.Join(dst, relPath)
		status, err := copyFile(path, dstPath, header)
		if err != nil {
			return err
		}

		switch status {
		case copyStatusAdded:
			result.Added = append(result.Added, relPath)
		case copyStatusUpdated:
			result.Updated = append(result.Updated, relPath)
		case copyStatusUnchanged:
			result.Unchanged = append(result.Unchanged, relPath)
		}
		return nil
	})

	if err != nil {
		return result, err
	}

	// Warn about missing fragments when filtering
	for name := range allowed {
		if !found[name] {
			fmt.Fprintf(os.Stderr, "Warning: local fragment not found: %s\n", name)
		}
	}

	return result, nil
}

// copyFromGitRepo clones a git repository and copies fragments from it.
// The repository should have YAML fragments in its root or in a context-fragments directory.
// If header is non-empty, it is prepended to YAML files.
func copyFromGitRepo(repoURL, destDir string, fragmentFilter []string, header string) (*resources.CopyResult, error) {
	// Create temporary directory for clone
	tmpDir, err := os.MkdirTemp("", "mlcm-git-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	fmt.Printf("Cloning %s...\n", repoURL)

	// Clone the repository (shallow clone for speed)
	if err := gitutil.ShallowClone(repoURL, tmpDir); err != nil {
		return nil, fmt.Errorf("git clone failed: %w", err)
	}

	// Look for fragments in standard locations
	var srcDir string
	candidates := []string{
		filepath.Join(tmpDir, "context-fragments"),
		filepath.Join(tmpDir, ".mlcm", "context-fragments"),
		tmpDir, // Root of repo
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			// Check if it contains YAML files
			hasYAML := false
			filepath.WalkDir(candidate, func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() && (strings.HasSuffix(d.Name(), ".yaml") || strings.HasSuffix(d.Name(), ".yml")) {
					hasYAML = true
					return filepath.SkipAll
				}
				return nil
			})
			if hasYAML {
				srcDir = candidate
				break
			}
		}
	}

	if srcDir == "" {
		return nil, fmt.Errorf("no YAML fragments found in repository")
	}

	return copyDir(srcDir, destDir, fragmentFilter, header)
}

// copyStatus indicates what happened when copying a file.
type copyStatus int

const (
	copyStatusAdded copyStatus = iota
	copyStatusUpdated
	copyStatusUnchanged
)

// copyFile copies a file as-is with read-only protection.
// If header is non-empty and the file is YAML, prepends the header.
// Returns the status of the copy operation.
func copyFile(src, dst string, header string) (copyStatus, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return 0, err
	}

	// Prepend header to YAML files if specified
	finalData := data
	if header != "" && (strings.HasSuffix(dst, ".yaml") || strings.HasSuffix(dst, ".yml")) {
		finalData = append([]byte(header), data...)
	}

	// Check if destination exists and compare content
	existing, err := os.ReadFile(dst)
	if err == nil {
		// File exists, check if content is the same
		if bytes.Equal(existing, finalData) {
			return copyStatusUnchanged, nil
		}
		// Content differs, will update
		if err := fsys.WriteProtected(dst, finalData); err != nil {
			return 0, err
		}
		return copyStatusUpdated, nil
	}

	// File doesn't exist, create it
	if err := fsys.WriteProtected(dst, finalData); err != nil {
		return 0, err
	}
	return copyStatusAdded, nil
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.AddCommand(initHomeCmd)

	initCmd.Flags().StringVar(&skipFragments, "skip-fragments", "", "Skip fragment sources: embedded (default), local, or both")
	initCmd.Flags().Lookup("skip-fragments").NoOptDefVal = "embedded"
	initCmd.Flags().StringVar(&fromGitRepo, "from-git", "", "Clone fragments from a git repository URL")
	initCmd.Flags().BoolVarP(&verboseInit, "verbose", "v", false, "List individual files being copied")
}
