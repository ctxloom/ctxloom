package cmd

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"mlcm/internal/config"
	"mlcm/resources"
)

var skipFragments string

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

Use --skip-fragments to control which sources are skipped:
  --skip-fragments          Skip embedded fragments (default)
  --skip-fragments=local    Skip ~/.mlcm fragments only
  --skip-fragments=both     Skip all fragment copying`,
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

	// Create config file
	configPath := filepath.Join(mlcmDir, config.ConfigFileName+".yaml")
	if err := writeProjectConfig(configPath, embeddedCfg, allPersonas, allGenerators, personas); err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}

	// Create .gitkeep for prompts directory (fragments has content)
	gitkeepPrompts := filepath.Join(promptsDir, ".gitkeep")
	if err := os.WriteFile(gitkeepPrompts, []byte(""), 0644); err != nil {
		return fmt.Errorf("failed to create .gitkeep: %w", err)
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

// copyDir recursively copies a directory tree.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		return copyFile(path, dstPath)
	})
}

// copyDirFiltered copies only files matching the fragment filter.
// fragmentFilter contains paths like "style/direct" (without .md extension).
// Missing fragments are warned about but do not cause failure.
func copyDirFiltered(src, dst string, fragmentFilter []string) error {
	// Build set of allowed fragments
	allowed := make(map[string]bool)
	for _, frag := range fragmentFilter {
		// Normalize: add .md if missing
		if !strings.HasSuffix(frag, ".md") {
			frag = frag + ".md"
		}
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

		// Check if this fragment is allowed
		if !allowed[relPath] {
			return nil
		}

		// Mark as found
		found[relPath] = true

		dstPath := filepath.Join(dst, relPath)
		return copyFile(path, dstPath)
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

// copyFile copies a single file, skipping if destination already exists.
func copyFile(src, dst string) error {
	// Skip if file already exists
	if _, err := os.Stat(dst); err == nil {
		return nil
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.AddCommand(initHomeCmd)

	initCmd.Flags().StringVar(&skipFragments, "skip-fragments", "", "Skip fragment sources: embedded (default), local, or both")
	initCmd.Flags().Lookup("skip-fragments").NoOptDefVal = "embedded"
}
