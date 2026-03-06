package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/profiles"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage profiles (named fragment collections)",
	Long: `Manage profiles - named collections of context fragments, bundles, and configuration.

Profiles are stored as YAML files in .scm/profiles/<name>.yaml and allow you to
quickly switch between different sets of context without specifying them individually.`,
}

var profileListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all profiles",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		profileDirs := profiles.GetProfileDirs(cfg.SCMPaths)
		if len(profileDirs) == 0 {
			fmt.Println("No profiles directory found.")
			fmt.Println("Create one with: mkdir -p .scm/profiles")
			return nil
		}

		loader := profiles.NewLoader(profileDirs)
		profileList, err := loader.List()
		if err != nil {
			return fmt.Errorf("failed to list profiles: %w", err)
		}

		if len(profileList) == 0 {
			fmt.Println("No profiles defined.")
			fmt.Println("Use 'scm profile add <name> -f <fragments...>' to create one.")
			return nil
		}

		// Get default profiles from config
		defaultProfiles := make(map[string]bool)
		for _, name := range cfg.Defaults.Profiles {
			defaultProfiles[name] = true
		}

		fmt.Printf("Profiles (%d):\n", len(profileList))
		for _, p := range profileList {
			fmt.Printf("  %s", p.Name)
			if defaultProfiles[p.Name] || p.Default {
				fmt.Printf(" (default)")
			}
			fmt.Println()
			if p.Description != "" {
				fmt.Printf("    %s\n", p.Description)
			}

			var parts []string
			if len(p.Parents) > 0 {
				parts = append(parts, fmt.Sprintf("parents: %s", strings.Join(p.Parents, ", ")))
			}
			if len(p.Bundles) > 0 {
				parts = append(parts, fmt.Sprintf("%d bundles", len(p.Bundles)))
			}
			if len(parts) > 0 {
				fmt.Printf("    %s\n", strings.Join(parts, ", "))
			}
		}

		return nil
	},
}

var (
	profileAddParents     []string
	profileAddBundles     []string
	profileAddDescription string
)

var profileAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a new profile",
	Long: `Add a new profile with bundles and/or parents.

Bundle references use full URLs:
  https://github.com/user/repo@v1/bundles/name    # Bundle from remote

Example:
  scm profile add developer -b https://github.com/user/scm@v1/bundles/go-development -d "Standard dev context"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if name == "help" {
			return cmd.Help()
		}

		if len(profileAddParents) == 0 && len(profileAddBundles) == 0 {
			return fmt.Errorf("at least one parent (--parent) or bundle (-b) is required")
		}

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		profileDirs := profiles.GetProfileDirs(cfg.SCMPaths)
		if len(profileDirs) == 0 {
			// Create the directory
			profileDirs = []string{filepath.Join(cfg.SCMPaths[0], "profiles")}
		}

		loader := profiles.NewLoader(profileDirs)

		if loader.Exists(name) {
			return fmt.Errorf("profile %q already exists (use 'profile remove' first)", name)
		}

		// Validate parent profiles exist
		for _, parent := range profileAddParents {
			if !loader.Exists(parent) {
				return fmt.Errorf("parent profile %q not found", parent)
			}
		}

		profile := &profiles.Profile{
			Name:        name,
			Description: profileAddDescription,
			Parents:     profileAddParents,
			Bundles:     profileAddBundles,
		}

		if err := loader.Save(profile); err != nil {
			return fmt.Errorf("failed to save profile: %w", err)
		}

		var parts []string
		if len(profileAddParents) > 0 {
			parts = append(parts, fmt.Sprintf("parents: %s", strings.Join(profileAddParents, ", ")))
		}
		if len(profileAddBundles) > 0 {
			parts = append(parts, fmt.Sprintf("bundles: %s", strings.Join(profileAddBundles, ", ")))
		}
		fmt.Printf("Created profile %q with %s\n", name, strings.Join(parts, "; "))
		fmt.Printf("Saved to: %s\n", profile.Path)
		return nil
	},
}

var profileRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove a profile",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if name == "help" {
			return cmd.Help()
		}

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		profileDirs := profiles.GetProfileDirs(cfg.SCMPaths)
		if len(profileDirs) == 0 {
			return fmt.Errorf("profile not found: no profiles directory")
		}

		loader := profiles.NewLoader(profileDirs)

		if err := loader.Delete(name); err != nil {
			return fmt.Errorf("failed to remove profile: %w", err)
		}

		fmt.Printf("Removed profile %q\n", name)
		return nil
	},
}

var profileShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show details of a profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if name == "help" {
			return cmd.Help()
		}

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		profileDirs := profiles.GetProfileDirs(cfg.SCMPaths)
		if len(profileDirs) == 0 {
			return fmt.Errorf("profile not found: no profiles directory")
		}

		loader := profiles.NewLoader(profileDirs)
		p, err := loader.Load(name)
		if err != nil {
			return fmt.Errorf("profile %q not found", name)
		}

		fmt.Printf("Profile: %s\n", p.Name)
		fmt.Printf("Path: %s\n", p.Path)
		isDefault := p.Default
		for _, defName := range cfg.Defaults.Profiles {
			if p.Name == defName {
				isDefault = true
				break
			}
		}
		if isDefault {
			fmt.Println("Default: yes")
		}
		if p.Description != "" {
			fmt.Printf("Description: %s\n", p.Description)
		}
		if len(p.Parents) > 0 {
			fmt.Println("Parents:")
			for _, parent := range p.Parents {
				fmt.Printf("  - %s\n", parent)
			}
		}
		if len(p.Bundles) > 0 {
			fmt.Println("Bundles:")
			for _, b := range p.Bundles {
				fmt.Printf("  - %s\n", b)
			}
		}
		if len(p.Tags) > 0 {
			fmt.Println("Tags:")
			for _, t := range p.Tags {
				fmt.Printf("  - %s\n", t)
			}
		}
		if len(p.Variables) > 0 {
			fmt.Println("Variables:")
			for k, v := range p.Variables {
				fmt.Printf("  %s: %s\n", k, v)
			}
		}

		return nil
	},
}

var profileUpdateCmd = &cobra.Command{
	Use:   "update <name>",
	Short: "Update a profile",
	Long: `Update an existing profile by adding or removing items.

Examples:
  scm profile update go-developer --add-parent https://github.com/user/scm@v1/profiles/developer
  scm profile update developer --add-bundle https://github.com/user/scm@v1/bundles/go-development
  scm profile update developer -d "New description"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if name == "help" {
			return cmd.Help()
		}

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		profileDirs := profiles.GetProfileDirs(cfg.SCMPaths)
		if len(profileDirs) == 0 {
			return fmt.Errorf("profile not found: no profiles directory")
		}

		loader := profiles.NewLoader(profileDirs)
		p, err := loader.Load(name)
		if err != nil {
			return fmt.Errorf("profile %q not found", name)
		}

		modified := false

		// Update description if provided
		if cmd.Flags().Changed("description") {
			p.Description = profileUpdateDescription
			modified = true
		}

		// Add parents
		for _, parent := range profileUpdateAddParents {
			if !slices.Contains(p.Parents, parent) {
				p.Parents = append(p.Parents, parent)
				fmt.Printf("Added parent: %s\n", parent)
				modified = true
			} else {
				fmt.Printf("Parent already present: %s\n", parent)
			}
		}

		// Remove parents
		for _, parent := range profileUpdateRemoveParents {
			if idx := slices.Index(p.Parents, parent); idx >= 0 {
				p.Parents = slices.Delete(p.Parents, idx, idx+1)
				fmt.Printf("Removed parent: %s\n", parent)
				modified = true
			} else {
				fmt.Printf("Parent not found: %s\n", parent)
			}
		}

		// Add bundles
		for _, b := range profileUpdateAddBundles {
			if !slices.Contains(p.Bundles, b) {
				p.Bundles = append(p.Bundles, b)
				fmt.Printf("Added bundle: %s\n", b)
				modified = true
			} else {
				fmt.Printf("Bundle already present: %s\n", b)
			}
		}

		// Remove bundles
		for _, b := range profileUpdateRemoveBundles {
			if idx := slices.Index(p.Bundles, b); idx >= 0 {
				p.Bundles = slices.Delete(p.Bundles, idx, idx+1)
				fmt.Printf("Removed bundle: %s\n", b)
				modified = true
			} else {
				fmt.Printf("Bundle not found: %s\n", b)
			}
		}

		if !modified {
			fmt.Println("No changes made.")
			return nil
		}

		if err := loader.Save(p); err != nil {
			return fmt.Errorf("failed to save profile: %w", err)
		}

		fmt.Printf("Updated profile %q\n", name)
		return nil
	},
}

var (
	profileUpdateAddParents    []string
	profileUpdateRemoveParents []string
	profileUpdateAddBundles    []string
	profileUpdateRemoveBundles []string
	profileUpdateDescription   string
)

var profileExportCmd = &cobra.Command{
	Use:   "export <name> <dest-dir>",
	Short: "Export a profile to a directory",
	Long: `Export a profile from .scm/profiles to an arbitrary directory.

Useful for publishing profiles to a shared repository like scm-github.

Examples:
  scm profile export architect ../scm-github/scm/v1/profiles
  scm profile export my-profile ./exports`,
	Args: cobra.ExactArgs(2),
	RunE: runProfileExport,
}

func runProfileExport(cmd *cobra.Command, args []string) error {
	name := args[0]
	destDir := args[1]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	profileDirs := profiles.GetProfileDirs(cfg.SCMPaths)
	if len(profileDirs) == 0 {
		return fmt.Errorf("profile not found: no profiles directory")
	}

	loader := profiles.NewLoader(profileDirs)
	profile, err := loader.Load(name)
	if err != nil {
		return fmt.Errorf("profile not found: %s", name)
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Read source file
	srcData, err := os.ReadFile(profile.Path)
	if err != nil {
		return fmt.Errorf("failed to read profile: %w", err)
	}

	// Write to destination
	destPath := filepath.Join(destDir, filepath.Base(profile.Path))
	if err := os.WriteFile(destPath, srcData, 0644); err != nil {
		return fmt.Errorf("failed to write profile: %w", err)
	}

	fmt.Printf("Exported: %s -> %s\n", profile.Path, destPath)
	return nil
}

var profileImportForce bool

var profileImportCmd = &cobra.Command{
	Use:   "import <path>",
	Short: "Import a profile from a local file",
	Long: `Import a profile YAML file into .scm/profiles.

Use --force to overwrite an existing profile.

Examples:
  scm profile import ../scm-github/scm/v1/profiles/architect.yaml
  scm profile import ./my-profile.yaml --force`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileImport,
}

func runProfileImport(cmd *cobra.Command, args []string) error {
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

	// Parse to validate it's valid YAML
	var profileData map[string]interface{}
	if err := yaml.Unmarshal(srcData, &profileData); err != nil {
		return fmt.Errorf("invalid profile file (not valid YAML): %w", err)
	}

	// Determine destination path
	profileDir := filepath.Join(cfg.SCMPaths[0], "profiles")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return fmt.Errorf("failed to create profiles directory: %w", err)
	}

	destPath := filepath.Join(profileDir, filepath.Base(srcPath))

	// Check if destination exists
	if _, err := os.Stat(destPath); err == nil && !profileImportForce {
		return fmt.Errorf("profile already exists: %s (use --force to overwrite)", destPath)
	}

	// Write to destination
	if err := os.WriteFile(destPath, srcData, 0644); err != nil {
		return fmt.Errorf("failed to write profile: %w", err)
	}

	fmt.Printf("Imported: %s -> %s\n", srcPath, destPath)
	return nil
}

func init() {
	rootCmd.AddCommand(profileCmd)

	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileAddCmd)
	profileCmd.AddCommand(profileRemoveCmd)
	profileCmd.AddCommand(profileShowCmd)
	profileCmd.AddCommand(profileUpdateCmd)
	profileCmd.AddCommand(profileExportCmd)
	profileCmd.AddCommand(profileImportCmd)

	profileAddCmd.Flags().StringSliceVar(&profileAddParents, "parent", nil, "Parent profile URL(s) to inherit from")
	profileAddCmd.Flags().StringSliceVarP(&profileAddBundles, "bundle", "b", nil, "Bundle URL(s) to include")
	profileAddCmd.Flags().StringVarP(&profileAddDescription, "description", "d", "", "Description of the profile")

	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddParents, "add-parent", nil, "Parent profile URL(s) to add")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveParents, "remove-parent", nil, "Parent profile URL(s) to remove")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddBundles, "add-bundle", nil, "Bundle URL(s) to add")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveBundles, "remove-bundle", nil, "Bundle URL(s) to remove")
	profileUpdateCmd.Flags().StringVarP(&profileUpdateDescription, "description", "d", "", "New description for the profile")

	profileImportCmd.Flags().BoolVarP(&profileImportForce, "force", "f", false, "Overwrite existing profile")

	// Register positional arg completions
	profileShowCmd.ValidArgsFunction = completeProfileNames
	profileRemoveCmd.ValidArgsFunction = completeProfileNames
	profileUpdateCmd.ValidArgsFunction = completeProfileNames
	profileExportCmd.ValidArgsFunction = completeProfileNames

	// Register flag completions
	_ = profileAddCmd.RegisterFlagCompletionFunc("parent", completeProfileNames)
	_ = profileUpdateCmd.RegisterFlagCompletionFunc("add-parent", completeProfileNames)
	_ = profileUpdateCmd.RegisterFlagCompletionFunc("remove-parent", completeProfileNames)
}

