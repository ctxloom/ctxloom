package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"mlcm/internal/config"
	"mlcm/internal/fsys"
	"mlcm/resources"
)

// yamlFragment represents the YAML structure of a fragment file.
type yamlFragment struct {
	Version   string            `yaml:"version,omitempty"`
	Author    string            `yaml:"author,omitempty"`
	Tags      []string          `yaml:"tags,omitempty"`
	Variables []string          `yaml:"variables,omitempty"`
	Content   string            `yaml:"content"`
	VarValues map[string]string `yaml:"var_values,omitempty"`
}

var skipFragments string
var fromGitRepo string

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
		pwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}
		mlcmDir := filepath.Join(pwd, config.MLCMDirName)
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
		fmt.Printf(".mlcm directory already exists at %s\n", mlcmDir)
		return nil
	}

	fmt.Printf("Initializing .mlcm directory at %s\n", mlcmDir)

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

	// Copy fragments (filtered by persona if specified)
	if len(personas) > 0 {
		fragments, err := config.CollectFragmentsForPersonas(embeddedCfg.Personas, personas)
		if err != nil {
			return err
		}
		if err := resources.CopySelectedFragments(fragmentsDir, fragments); err != nil {
			return fmt.Errorf("failed to copy selected fragments: %w", err)
		}
		fmt.Printf("Copied %d fragments for personas: %s\n", len(fragments), strings.Join(personas, ", "))
	} else {
		if err := resources.CopyFragments(fragmentsDir); err != nil {
			return fmt.Errorf("failed to copy embedded fragments: %w", err)
		}
		fmt.Println("Copied all embedded context fragments")
	}

	// Create config file with personas and generators
	configPath := filepath.Join(mlcmDir, config.ConfigFileName+".yaml")
	if err := writeHomeConfig(configPath, embeddedCfg, personas); err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}

	// Create .gitkeep for prompts directory
	gitkeepPrompts := filepath.Join(promptsDir, ".gitkeep")
	if err := os.WriteFile(gitkeepPrompts, []byte(""), 0644); err != nil {
		return fmt.Errorf("failed to create .gitkeep: %w", err)
	}

	fmt.Printf("\nMLCM home initialized successfully!\n")
	fmt.Printf("  Context fragments: %s\n", fragmentsDir)
	fmt.Printf("  Prompts:           %s\n", promptsDir)
	fmt.Printf("  Config:            %s\n", configPath)

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
	if info, err := os.Stat(mlcmDir); err == nil && info.IsDir() {
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
	}

	// Copy fragments based on --skip-fragments setting
	skipEmbedded := skipFragments == "embedded" || skipFragments == "both"
	skipLocal := skipFragments == "local" || skipFragments == "both"

	// First, copy embedded fragments
	if !skipEmbedded {
		if len(fragmentFilter) > 0 {
			if err := resources.CopySelectedFragments(fragmentsDir, fragmentFilter); err != nil {
				return fmt.Errorf("failed to copy embedded fragments: %w", err)
			}
			fmt.Printf("Copied %d embedded fragments for personas: %s\n", len(fragmentFilter), strings.Join(personas, ", "))
		} else {
			if err := resources.CopyFragments(fragmentsDir); err != nil {
				return fmt.Errorf("failed to copy embedded fragments: %w", err)
			}
			fmt.Println("Copied embedded context fragments")
		}
	}

	// Then, copy from ~/.mlcm (overwrites duplicates)
	if !skipLocal {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		homeFragments := filepath.Join(home, config.MLCMDirName, config.ContextFragmentsDir)
		if info, err := os.Stat(homeFragments); err == nil && info.IsDir() {
			if len(fragmentFilter) > 0 {
				if err := copyDirFiltered(homeFragments, fragmentsDir, fragmentFilter); err != nil {
					return fmt.Errorf("failed to copy fragments from %s: %w", homeFragments, err)
				}
				fmt.Printf("Copied filtered fragments from %s\n", homeFragments)
			} else {
				if err := copyDir(homeFragments, fragmentsDir); err != nil {
					return fmt.Errorf("failed to copy fragments from %s: %w", homeFragments, err)
				}
				fmt.Printf("Copied fragments from %s\n", homeFragments)
			}
		}
	}

	// Copy from git repository if specified (overwrites duplicates)
	if fromGitRepo != "" {
		if err := copyFromGitRepo(fromGitRepo, fragmentsDir, fragmentFilter); err != nil {
			return fmt.Errorf("failed to copy from git repo: %w", err)
		}
	}

	// Copy prompts from ~/.mlcm/prompts/ (if they exist)
	if !skipLocal {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		homePrompts := filepath.Join(home, config.MLCMDirName, config.PromptsDir)
		if info, err := os.Stat(homePrompts); err == nil && info.IsDir() {
			if err := copyDir(homePrompts, promptsDir); err != nil {
				return fmt.Errorf("failed to copy prompts from %s: %w", homePrompts, err)
			}
			fmt.Printf("Copied prompts from %s\n", homePrompts)
		}
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

	fmt.Printf("\nMLCM initialized successfully!\n")
	fmt.Printf("  Context fragments: %s\n", fragmentsDir)
	fmt.Printf("  Prompts:           %s\n", promptsDir)
	fmt.Printf("  Config:            %s\n", configPath)

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

// distilledSuffix is the suffix for distilled fragment files.
const distilledSuffix = ".distilled.yaml"

// copyDir recursively copies a directory tree, converting YAML fragments to markdown.
// Also copies .distilled.yaml files if they exist.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, relPath), 0755)
		}

		name := d.Name()

		// Handle .distilled.md files - copy as-is
		if strings.HasSuffix(name, distilledSuffix) {
			dstPath := filepath.Join(dst, relPath)
			return copyDistilledFile(path, dstPath)
		}

		// Convert .yaml/.yml to .md for destination
		dstRelPath := relPath
		if strings.HasSuffix(dstRelPath, ".yaml") {
			dstRelPath = strings.TrimSuffix(dstRelPath, ".yaml") + ".md"
		} else if strings.HasSuffix(dstRelPath, ".yml") {
			dstRelPath = strings.TrimSuffix(dstRelPath, ".yml") + ".md"
		} else {
			// Skip non-YAML files that aren't distilled
			return nil
		}
		dstPath := filepath.Join(dst, dstRelPath)

		return copyFragmentFile(path, dstPath)
	})
}

// copyDirFiltered copies only files matching the fragment filter.
// fragmentFilter contains paths like "style/direct" (without extension).
// Source files are YAML, output files are markdown.
// Also copies .distilled.yaml files if they exist.
// Missing fragments are warned about but do not cause failure.
func copyDirFiltered(src, dst string, fragmentFilter []string) error {
	// Build set of allowed fragments (normalized without extension)
	allowed := make(map[string]bool)
	for _, frag := range fragmentFilter {
		// Strip any extension
		frag = strings.TrimSuffix(frag, distilledSuffix)
		frag = strings.TrimSuffix(frag, ".md")
		frag = strings.TrimSuffix(frag, ".yaml")
		frag = strings.TrimSuffix(frag, ".yml")
		allowed[frag] = true
	}

	// Track which fragments were found
	found := make(map[string]bool)

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil // Directories are created as needed
		}

		name := d.Name()

		// Handle .distilled.md files
		if strings.HasSuffix(name, distilledSuffix) {
			// Get base name for checking if allowed
			baseName := strings.TrimSuffix(relPath, distilledSuffix)
			if !allowed[baseName] {
				return nil
			}
			dstPath := filepath.Join(dst, relPath)
			return copyDistilledFile(path, dstPath)
		}

		// Get base name without extension for comparison
		baseName := relPath
		if strings.HasSuffix(baseName, ".yaml") {
			baseName = strings.TrimSuffix(baseName, ".yaml")
		} else if strings.HasSuffix(baseName, ".yml") {
			baseName = strings.TrimSuffix(baseName, ".yml")
		} else {
			// Skip non-YAML files
			return nil
		}

		// Check if this fragment is allowed
		if !allowed[baseName] {
			return nil
		}

		// Mark as found
		found[baseName] = true

		// Convert to .md for output
		dstPath := filepath.Join(dst, baseName+".md")
		return copyFragmentFile(path, dstPath)
	})

	if err != nil {
		return err
	}

	// Warn about missing fragments
	for name := range allowed {
		if !found[name] {
			fmt.Fprintf(os.Stderr, "Warning: local fragment not found: %s\n", name)
		}
	}

	return nil
}

// copyFromGitRepo clones a git repository and copies fragments from it.
// The repository should have YAML fragments in its root or in a context-fragments directory.
func copyFromGitRepo(repoURL, destDir string, fragmentFilter []string) error {
	// Create temporary directory for clone
	tmpDir, err := os.MkdirTemp("", "mlcm-git-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	fmt.Printf("Cloning %s...\n", repoURL)

	// Clone the repository (shallow clone for speed)
	cmd := exec.Command("git", "clone", "--depth", "1", repoURL, tmpDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
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
		return fmt.Errorf("no YAML fragments found in repository")
	}

	// Copy fragments
	if len(fragmentFilter) > 0 {
		if err := copyDirFiltered(srcDir, destDir, fragmentFilter); err != nil {
			return err
		}
		fmt.Printf("Copied filtered fragments from git repository\n")
	} else {
		if err := copyDir(srcDir, destDir); err != nil {
			return err
		}
		fmt.Printf("Copied fragments from git repository\n")
	}

	return nil
}

// copyFragmentFile reads a YAML fragment file and writes it as markdown.
// The destination file is set to read-only to protect from accidental edits.
func copyFragmentFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	// Parse YAML to extract content
	var frag yamlFragment
	if err := yaml.Unmarshal(data, &frag); err != nil {
		return fmt.Errorf("failed to parse %s: %w", src, err)
	}

	// Write content as markdown with protection
	content := strings.TrimSpace(frag.Content) + "\n"
	return fsys.WriteProtected(dst, []byte(content))
}

// copyDistilledFile copies a distilled YAML file as-is.
// The destination file is set to read-only to protect from accidental edits.
func copyDistilledFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	// Write file with protection
	return fsys.WriteProtected(dst, data)
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.AddCommand(initHomeCmd)

	initCmd.Flags().StringVar(&skipFragments, "skip-fragments", "", "Skip fragment sources: embedded (default), local, or both")
	initCmd.Flags().Lookup("skip-fragments").NoOptDefVal = "embedded"
	initCmd.Flags().StringVar(&fromGitRepo, "from-git", "", "Clone fragments from a git repository URL")
}
