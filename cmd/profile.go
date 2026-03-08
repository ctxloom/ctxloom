package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/SophisticatedContextManager/scm/internal/operations"
	"github.com/SophisticatedContextManager/scm/internal/profiles"
	"github.com/SophisticatedContextManager/scm/internal/remote"
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
	profileCreateParents     []string
	profileCreateBundles     []string
	profileCreateDescription string
)

var profileCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new profile",
	Long: `Create a new profile with bundles and/or parents.

Bundle references use full URLs:
  https://github.com/user/repo@v1/bundles/name    # Bundle from remote

Example:
  scm profile create developer -b https://github.com/user/scm@v1/bundles/go-development -d "Standard dev context"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if name == "help" {
			return cmd.Help()
		}

		if len(profileCreateParents) == 0 && len(profileCreateBundles) == 0 {
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
			return fmt.Errorf("profile %q already exists (use 'profile delete' first)", name)
		}

		// Validate parent profiles exist
		for _, parent := range profileCreateParents {
			if !loader.Exists(parent) {
				return fmt.Errorf("parent profile %q not found", parent)
			}
		}

		profile := &profiles.Profile{
			Name:        name,
			Description: profileCreateDescription,
			Parents:     profileCreateParents,
			Bundles:     profileCreateBundles,
		}

		if err := loader.Save(profile); err != nil {
			return fmt.Errorf("failed to save profile: %w", err)
		}

		var parts []string
		if len(profileCreateParents) > 0 {
			parts = append(parts, fmt.Sprintf("parents: %s", strings.Join(profileCreateParents, ", ")))
		}
		if len(profileCreateBundles) > 0 {
			parts = append(parts, fmt.Sprintf("bundles: %s", strings.Join(profileCreateBundles, ", ")))
		}
		fmt.Printf("Created profile %q with %s\n", name, strings.Join(parts, "; "))
		fmt.Printf("Saved to: %s\n", profile.Path)
		return nil
	},
}

var profileDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a profile",
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
			return fmt.Errorf("failed to delete profile: %w", err)
		}

		fmt.Printf("Deleted profile %q\n", name)
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
	Use:   "modify <name>",
	Short: "Modify a profile's configuration",
	Long: `Modify an existing profile by adding or removing items.

Examples:
  scm profile modify go-developer --add-parent https://github.com/user/scm@v1/profiles/developer
  scm profile modify developer --add-bundle https://github.com/user/scm@v1/bundles/go-development
  scm profile modify developer -d "New description"`,
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

		fmt.Printf("Modified profile %q\n", name)
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

var (
	profilePushPR      bool
	profilePushBranch  string
	profilePushMessage string
)

var profilePushCmd = &cobra.Command{
	Use:   "push <name> [remote]",
	Short: "Publish a profile to a remote repository",
	Long: `Publish a local profile to a remote repository.

By default, publishes directly to the default branch. Use --pr to create
a pull request instead.

If no remote is specified, uses the default remote.

Examples:
  scm profile push my-profile
  scm profile push my-profile scm-github
  scm profile push my-profile --pr
  scm profile push my-profile scm-github --message "Add my profile"`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runProfilePush,
}

func runProfilePush(cmd *cobra.Command, args []string) error {
	profileName := args[0]
	remoteName := ""
	if len(args) > 1 {
		remoteName = args[1]
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Load the profile
	profileDirs := profiles.GetProfileDirs(cfg.SCMPaths)
	if len(profileDirs) == 0 {
		return fmt.Errorf("no profiles directory found")
	}

	loader := profiles.NewLoader(profileDirs)
	profile, err := loader.Load(profileName)
	if err != nil {
		return fmt.Errorf("profile not found: %s", profileName)
	}

	// Initialize registry
	registry, err := remote.NewRegistry("")
	if err != nil {
		return fmt.Errorf("failed to initialize registry: %w", err)
	}

	// Use default remote if not specified
	if remoteName == "" {
		remoteName = registry.GetDefault()
		if remoteName == "" {
			return fmt.Errorf("no remote specified and no default set. Use: scm profile push <name> <remote>")
		}
	}

	auth := remote.LoadAuth("")

	// Build publish options
	opts := remote.PublishOptions{
		CreatePR: profilePushPR,
		Branch:   profilePushBranch,
		Message:  profilePushMessage,
		ItemType: remote.ItemTypeProfile,
	}

	fmt.Printf("Publishing profile %q to %s...\n", profileName, remoteName)

	result, err := remote.Publish(cmd.Context(), profile.Path, remoteName, opts, registry, auth)
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

var profileEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit a profile",
	Long: `Edit a profile's YAML file using your configured editor.

Examples:
  scm profile edit my-profile`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return editProfileFile(args[0])
	},
}

var (
	profileInstallForce bool
	profileInstallBlind bool
)

var profileInstallCmd = &cobra.Command{
	Use:   "install <reference>",
	Short: "Install a profile from remote",
	Long: `Install a profile from a remote repository.

Reference formats:
  scm-github/developer                    # Profile from default remote path
  https://github.com/user/repo@v1/profiles/developer   # Full URL

Examples:
  scm profile install scm-github/developer
  scm profile install scm-github/architect`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		result, err := operations.PullItem(cmd.Context(), cfg, operations.PullItemRequest{
			Reference: args[0],
			ItemType:  "profile",
			Force:     profileInstallForce,
			Blind:     profileInstallBlind,
		})
		if err != nil {
			return err
		}

		action := "Installed"
		if result.Overwritten {
			action = "Updated"
		}

		fmt.Printf("%s profile: %s\n", action, result.LocalPath)
		fmt.Printf("SHA: %s\n", result.SHA[:7])

		return nil
	},
}

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
	profileCmd.AddCommand(profileCreateCmd)
	profileCmd.AddCommand(profileDeleteCmd)
	profileCmd.AddCommand(profileShowCmd)
	profileCmd.AddCommand(profileEditCmd)
	profileCmd.AddCommand(profileUpdateCmd)
	profileCmd.AddCommand(profileInstallCmd)
	profileCmd.AddCommand(profilePushCmd)
	profileCmd.AddCommand(profileExportCmd)
	profileCmd.AddCommand(profileImportCmd)

	profileCreateCmd.Flags().StringSliceVar(&profileCreateParents, "parent", nil, "Parent profile URL(s) to inherit from")
	profileCreateCmd.Flags().StringSliceVarP(&profileCreateBundles, "bundle", "b", nil, "Bundle URL(s) to include")
	profileCreateCmd.Flags().StringVarP(&profileCreateDescription, "description", "d", "", "Description of the profile")

	profilePushCmd.Flags().BoolVar(&profilePushPR, "pr", false, "Create a pull request instead of pushing directly")
	profilePushCmd.Flags().StringVar(&profilePushBranch, "branch", "", "Target branch (default: repository default)")
	profilePushCmd.Flags().StringVarP(&profilePushMessage, "message", "m", "", "Commit message")

	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddParents, "add-parent", nil, "Parent profile URL(s) to add")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveParents, "remove-parent", nil, "Parent profile URL(s) to remove")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddBundles, "add-bundle", nil, "Bundle URL(s) to add")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveBundles, "remove-bundle", nil, "Bundle URL(s) to remove")
	profileUpdateCmd.Flags().StringVarP(&profileUpdateDescription, "description", "d", "", "New description for the profile")

	profileImportCmd.Flags().BoolVarP(&profileImportForce, "force", "f", false, "Overwrite existing profile")

	profileInstallCmd.Flags().BoolVarP(&profileInstallForce, "force", "f", false, "Skip confirmation prompts")
	profileInstallCmd.Flags().BoolVar(&profileInstallBlind, "blind", false, "Skip security review display")

	// Register positional arg completions
	profileShowCmd.ValidArgsFunction = completeProfileNames
	profileDeleteCmd.ValidArgsFunction = completeProfileNames
	profileEditCmd.ValidArgsFunction = completeProfileNames
	profileUpdateCmd.ValidArgsFunction = completeProfileNames
	profilePushCmd.ValidArgsFunction = completeProfileNames
	profileExportCmd.ValidArgsFunction = completeProfileNames

	// Register flag completions
	_ = profileCreateCmd.RegisterFlagCompletionFunc("parent", completeProfileNames)
	_ = profileUpdateCmd.RegisterFlagCompletionFunc("add-parent", completeProfileNames)
	_ = profileUpdateCmd.RegisterFlagCompletionFunc("remove-parent", completeProfileNames)
}

