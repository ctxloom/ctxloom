package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/iox"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage profiles (named fragment collections)",
	Long: `Manage profiles - named collections of context fragments, bundles, and configuration.

Profiles are stored as YAML files in .ctxloom/profiles/<name>.yaml and allow you to
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

		profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
		if len(profileDirs) == 0 {
			fmt.Println("No profiles directory found.")
			fmt.Println("Create one with: mkdir -p .ctxloom/profiles")
			return nil
		}

		loader := profiles.NewLoader(profileDirs)
		profileList, err := loader.List()
		if err != nil {
			return fmt.Errorf("failed to list profiles: %w", err)
		}

		if len(profileList) == 0 {
			fmt.Println("No profiles defined.")
			fmt.Println("Use 'ctxloom profile add <name> -f <fragments...>' to create one.")
			return nil
		}

		defaultProfiles := make(map[string]bool)
		for _, name := range cfg.Defaults.Profiles {
			defaultProfiles[name] = true
		}
		return renderProfileList(cmd.OutOrStdout(), profileList, defaultProfiles)
	},
}

// renderProfileList writes the human-readable summary of a profile list
// to out. Extracted from profileListCmd's RunE so the formatting decisions
// (default-tag, parents/bundles line, description indentation) are
// testable without invoking cobra or touching the real config.
func renderProfileList(out io.Writer, list []*profiles.Profile, defaults map[string]bool) error {
	w := iox.NewErrWriter(out)
	w.Printf("Profiles (%d):\n", len(list))
	for _, p := range list {
		w.Printf("  %s", p.Name)
		if defaults[p.Name] {
			w.Printf(" (default)")
		}
		w.Println()
		if p.Description != "" {
			w.Printf("    %s\n", p.Description)
		}

		var parts []string
		if len(p.Parents) > 0 {
			parts = append(parts, fmt.Sprintf("parents: %s", strings.Join(p.Parents, ", ")))
		}
		if len(p.Bundles) > 0 {
			parts = append(parts, fmt.Sprintf("%d bundles", len(p.Bundles)))
		}
		if len(parts) > 0 {
			w.Printf("    %s\n", strings.Join(parts, ", "))
		}
	}
	return w.Err()
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
  ctxloom profile create developer -b https://github.com/user/ctxloom@v1/bundles/go-development -d "Standard dev context"`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileCreate,
}

func runProfileCreate(cmd *cobra.Command, args []string) error {
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

	// Route through the operations core so the CLI shares the MCP path's
	// validation and the default auto-promotion (so `ctxloom run` doesn't
	// launch with empty context after the first profile is created). The
	// pre-built loader preserves the CLI's "default to <appPath>/profiles when
	// none configured" behavior.
	res, err := operations.CreateProfile(cmd.Context(), cfg, operations.CreateProfileRequest{
		Name:        name,
		Description: profileCreateDescription,
		Parents:     profileCreateParents,
		Bundles:     profileCreateBundles,
		Loader:      profiles.NewLoader(profileCreateDirs(cfg)),
	})
	if err != nil {
		return err
	}

	printProfileCreated(name, res.Path)
	return nil
}

// profileCreateDirs returns the profile directories, defaulting to
// <appPath>/profiles when none are configured yet.
func profileCreateDirs(cfg *config.Config) []string {
	dirs := profiles.GetProfileDirs(cfg.AppPaths)
	if len(dirs) == 0 {
		dirs = []string{filepath.Join(cfg.AppPaths[0], "profiles")}
	}
	return dirs
}

// printProfileCreated reports a newly-created profile's parents/bundles and path.
func printProfileCreated(name, path string) {
	var parts []string
	if len(profileCreateParents) > 0 {
		parts = append(parts, fmt.Sprintf("parents: %s", strings.Join(profileCreateParents, ", ")))
	}
	if len(profileCreateBundles) > 0 {
		parts = append(parts, fmt.Sprintf("bundles: %s", strings.Join(profileCreateBundles, ", ")))
	}
	fmt.Printf("Created profile %q with %s\n", name, strings.Join(parts, "; "))
	fmt.Printf("Saved to: %s\n", path)
}

var profileDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a profile",
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

		// Operations core deletes the profile AND clears it from the config
		// defaults if it was the default — a cleanup the old CLI path skipped.
		if _, err := operations.DeleteProfile(cmd.Context(), cfg, operations.DeleteProfileRequest{Name: name}); err != nil {
			return err
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

		profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
		if len(profileDirs) == 0 {
			return fmt.Errorf("profile not found: no profiles directory")
		}

		loader := profiles.NewLoader(profileDirs)
		p, err := loader.Load(name)
		if err != nil {
			return fmt.Errorf("profile %q not found", name)
		}
		return renderProfileShow(cmd.OutOrStdout(), p, cfg.Defaults.IsDefaultProfile(p.Name))
	},
}

// renderProfileShow writes the human-readable detail view of one profile
// to out. Each optional section (description, parents, bundles, tags,
// variables, exclude_*) is suppressed when empty. Extracted from
// profileShowCmd's RunE.
func renderProfileShow(out io.Writer, p *profiles.Profile, isDefault bool) error {
	w := iox.NewErrWriter(out)
	w.Printf("Profile: %s\n", p.Name)
	w.Printf("Path: %s\n", p.Path)
	if isDefault {
		w.Println("Default: yes")
	}
	if p.Description != "" {
		w.Printf("Description: %s\n", p.Description)
	}
	writeBulletList(w, "Parents", p.Parents)
	writeBulletList(w, "Bundles", p.Bundles)
	writeBulletList(w, "Tags", p.Tags)
	if len(p.Variables) > 0 {
		w.Println("Variables:")
		for k, v := range p.Variables {
			w.Printf("  %s: %s\n", k, v)
		}
	}
	writeBulletList(w, "Excluded fragments", p.ExcludeFragments)
	writeBulletList(w, "Excluded prompts", p.ExcludePrompts)
	writeBulletList(w, "Excluded MCP servers", p.ExcludeMCP)
	return w.Err()
}

func writeBulletList(w *iox.ErrWriter, heading string, items []string) {
	if len(items) == 0 {
		return
	}
	w.Printf("%s:\n", heading)
	for _, item := range items {
		w.Printf("  - %s\n", item)
	}
}

var profileUpdateCmd = &cobra.Command{
	Use:   "modify <name>",
	Short: "Modify a profile's configuration",
	Long: `Modify an existing profile by adding or removing items.

Examples:
  ctxloom profile modify go-developer --add-parent https://github.com/user/ctxloom@v1/profiles/developer
  ctxloom profile modify developer --add-bundle https://github.com/user/ctxloom@v1/bundles/go-development
  ctxloom profile modify developer -d "New description"`,
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

		req := operations.UpdateProfileRequest{
			Name:                   name,
			AddParents:             profileUpdateAddParents,
			RemoveParents:          profileUpdateRemoveParents,
			AddBundles:             profileUpdateAddBundles,
			RemoveBundles:          profileUpdateRemoveBundles,
			AddExcludeFragments:    profileUpdateAddExcludeFragments,
			RemoveExcludeFragments: profileUpdateRemoveExcludeFragments,
			AddExcludePrompts:      profileUpdateAddExcludePrompts,
			RemoveExcludePrompts:   profileUpdateRemoveExcludePrompts,
			AddExcludeMCP:          profileUpdateAddExcludeMCP,
			RemoveExcludeMCP:       profileUpdateRemoveExcludeMCP,
		}
		if cmd.Flags().Changed("description") {
			d := profileUpdateDescription
			req.Description = &d
		}

		// Route through the operations core: it validates added parents exist
		// before mutating (a check the old CLI path lacked) and reflects the
		// default flag into config.
		res, err := operations.UpdateProfile(cmd.Context(), cfg, req)
		if err != nil {
			return err
		}

		w := iox.NewErrWriter(cmd.OutOrStdout())
		if res.Status == "no_changes" {
			w.Println("No changes made.")
			return w.Err()
		}
		for _, c := range res.Changes {
			w.Printf("%s\n", c)
		}
		w.Printf("Modified profile %q\n", name)
		return w.Err()
	},
}

var (
	profileUpdateAddParents             []string
	profileUpdateRemoveParents          []string
	profileUpdateAddBundles             []string
	profileUpdateRemoveBundles          []string
	profileUpdateDescription            string
	profileUpdateAddExcludeFragments    []string
	profileUpdateRemoveExcludeFragments []string
	profileUpdateAddExcludePrompts      []string
	profileUpdateRemoveExcludePrompts   []string
	profileUpdateAddExcludeMCP          []string
	profileUpdateRemoveExcludeMCP       []string
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
  ctxloom profile push my-profile
  ctxloom profile push my-profile ctxloom-default
  ctxloom profile push my-profile --pr
  ctxloom profile push my-profile ctxloom-default --message "Add my profile"`,
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

	profile, err := loadProfileForPush(cfg, profileName)
	if err != nil {
		return err
	}

	registry, err := remote.NewRegistry("")
	if err != nil {
		return fmt.Errorf("failed to initialize registry: %w", err)
	}

	remoteName, err = resolveDefaultRemote(registry, remoteName, "ctxloom profile push <name> <remote>")
	if err != nil {
		return err
	}

	opts := remote.PublishOptions{
		CreatePR: profilePushPR,
		Branch:   profilePushBranch,
		Message:  profilePushMessage,
		ItemType: remote.ItemTypeProfile,
	}
	fmt.Printf("Publishing profile %q to %s...\n", profileName, remoteName)

	pm := remote.NewPublishManager(registry, remote.LoadAuth(""))
	result, err := pm.Publish(cmd.Context(), profile.Path, remoteName, opts)
	if err != nil {
		return err
	}

	printPublishResult(result)
	return nil
}

// loadProfileForPush loads the named profile from the configured profile dirs.
func loadProfileForPush(cfg *config.Config, profileName string) (*profiles.Profile, error) {
	profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
	if len(profileDirs) == 0 {
		return nil, fmt.Errorf("no profiles directory found")
	}
	profile, err := profiles.NewLoader(profileDirs).Load(profileName)
	if err != nil {
		return nil, fmt.Errorf("profile not found: %s", profileName)
	}
	return profile, nil
}

// resolveDefaultRemote returns remoteName when set, else the registry default,
// erroring (with the given usage hint) when neither is available. Shared by the
// profile and bundle push commands.
func resolveDefaultRemote(registry *remote.Registry, remoteName, usage string) (string, error) {
	if remoteName != "" {
		return remoteName, nil
	}
	def := registry.GetDefault()
	if def == "" {
		return "", fmt.Errorf("no remote specified and no default set. Use: %s", usage)
	}
	return def, nil
}

// printPublishResult reports a publish outcome: the PR URL, or the created/
// updated path and commit. Shared by the profile and bundle push commands.
func printPublishResult(result *remote.PublishResult) {
	if result.PRURL != "" {
		fmt.Printf("Created pull request: %s\n", result.PRURL)
		return
	}
	action := "Created"
	if !result.Created {
		action = "Updated"
	}
	fmt.Printf("%s %s\n", action, result.Path)
	fmt.Printf("Commit: %s\n", result.SHA[:7])
}

var profileEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit a profile",
	Long: `Edit a profile's YAML file using your configured editor.

Examples:
  ctxloom profile edit my-profile`,
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
  ctxloom-default/developer                    # Profile from default remote path
  https://github.com/user/repo@v1/profiles/developer   # Full URL

Examples:
  ctxloom profile install ctxloom-default/developer
  ctxloom profile install ctxloom-default/architect`,
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
	Long: `Export a profile from .ctxloom/profiles to an arbitrary directory.

Useful for publishing profiles to a shared repository like ctxloom-default.

Examples:
  ctxloom profile export architect ../ctxloom-default/ctxloom/profiles
  ctxloom profile export my-profile ./exports`,
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

	profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
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
	Long: `Import a profile YAML file into .ctxloom/profiles.

Use --force to overwrite an existing profile.

Examples:
  ctxloom profile import ../ctxloom-default/ctxloom/profiles/architect.yaml
  ctxloom profile import ./my-profile.yaml --force`,
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

	// Parse against the Profile schema so we catch malformed profiles
	// (not just unparseable YAML) before copying them. The parsed value
	// is discarded — we only copy the source file on disk — but
	// validating shape surfaces "this isn't a profile" errors at import
	// time instead of at the next ctxloom run.
	var profileData config.Profile
	if err := yaml.Unmarshal(srcData, &profileData); err != nil {
		return fmt.Errorf("invalid profile file: %w", err)
	}

	// Determine destination path
	profileDir := filepath.Join(cfg.AppPaths[0], "profiles")
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
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddExcludeFragments, "exclude-fragment", nil, "Fragment name(s) to exclude")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveExcludeFragments, "include-fragment", nil, "Fragment name(s) to stop excluding")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddExcludePrompts, "exclude-prompt", nil, "Prompt name(s) to exclude")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveExcludePrompts, "include-prompt", nil, "Prompt name(s) to stop excluding")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddExcludeMCP, "exclude-mcp", nil, "MCP server name(s) to exclude")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveExcludeMCP, "include-mcp", nil, "MCP server name(s) to stop excluding")

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
