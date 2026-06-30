package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
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

		// The directory-existence check stays a CLI concern: it distinguishes
		// "no profiles dir at all" (suggest mkdir) from "dir exists but empty"
		// (suggest create). operations.ListProfiles would conflate the two by
		// defaulting the dir. Resolve the structured list up front so --format
		// json is honored uniformly (emitting [] in both empty cases); the human
		// path keeps the dir-vs-empty hint messages.
		profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
		var list []operations.ProfileEntry
		if len(profileDirs) > 0 {
			res, err := operations.ListProfiles(cmd.Context(), cfg, operations.ListProfilesRequest{})
			if err != nil {
				return err
			}
			list = res.Profiles
		}
		if list == nil {
			list = []operations.ProfileEntry{}
		}

		return emit(cmd, list, func() error {
			out := cmd.OutOrStdout()
			if len(profileDirs) == 0 {
				fmt.Fprintln(out, "No profiles directory found.")
				fmt.Fprintln(out, "Create one with: mkdir -p .ctxloom/profiles")
				return nil
			}
			if len(list) == 0 {
				fmt.Fprintln(out, "No profiles defined.")
				fmt.Fprintln(out, "Use 'ctxloom profile create <name> -b <bundles...>' to create one.")
				return nil
			}
			return renderProfileList(out, list)
		})
	},
}

// renderProfileList writes the human-readable summary of a profile list
// to out. Extracted from profileListCmd's RunE so the formatting decisions
// (default-tag, parents/bundles line, description indentation) are
// testable without invoking cobra or touching the real config. The
// per-entry Default flag is resolved by the operations layer.
func renderProfileList(out io.Writer, list []operations.ProfileEntry) error {
	w := iox.NewErrWriter(out)
	w.Printf("Profiles (%d):\n", len(list))
	for _, p := range list {
		w.Printf("  %s", p.Name)
		if p.Default {
			w.Printf(" (default)")
		}
		w.Println()
		if p.Description != "" {
			w.Printf("    %s\n", p.Description)
		}

		var parts []string
		if p.Bundle != "" {
			parts = append(parts, fmt.Sprintf("from bundle: %s", p.Bundle))
		}
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
	profileCreateLLM         string
)

var profileCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new profile",
	Long: `Create a new profile with bundles and/or parents.

Bundle references use full URLs:
  https://github.com/user/repo@bundles/name    # Bundle from remote

Example:
  ctxloom profile create developer -b https://github.com/user/ctxloom@bundles/go-development -d "Standard dev context"`,
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

	// Validate --llm up front the same way `run -l` does (friction-up-front):
	// an unknown label/backend is rejected at create time rather than warned
	// about on every launch.
	if profileCreateLLM != "" {
		if _, err := validateExplicitLLM(cfg, profileCreateLLM); err != nil {
			return err
		}
	}

	// Expand bare convenience refs (e.g. "code-review-base#fragments/conduct")
	// against the configured default remote so the profile stores canonical
	// URLs. Scheme-qualified refs pass through untouched.
	registry := defaultRemoteRegistry()
	profileCreateParents = expandRefsAgainstDefaultRemote(profileCreateParents, remote.ItemTypeProfile, registry)
	profileCreateBundles = expandRefsAgainstDefaultRemote(profileCreateBundles, remote.ItemTypeBundle, registry)

	// Route through the operations core so the CLI shares the MCP path's
	// validation and the default auto-promotion (so `ctxloom run` doesn't
	// launch with empty context after the first profile is created). The
	// pre-built loader preserves the CLI's "default to <appPath>/profiles when
	// none configured" behavior.
	res, err := operations.CreateProfile(cmd.Context(), cfg, operations.CreateProfileRequest{
		Name:        name,
		Description: profileCreateDescription,
		LLM:         profileCreateLLM,
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
		// Mirror the len(AppPaths)==0 guard the rest of internal/config uses
		// before indexing AppPaths[0]; a directly-constructed Config can carry
		// an empty slice, which would otherwise panic here.
		if len(cfg.AppPaths) == 0 {
			return nil
		}
		dirs = []string{filepath.Join(cfg.AppPaths[0], "profiles")}
	}
	return dirs
}

// defaultRemoteRegistry loads the remote registry for ref expansion, returning
// nil on any error. Expansion is a convenience: a missing/unreadable registry
// simply means bare refs pass through unchanged rather than failing the command.
func defaultRemoteRegistry() *remote.Registry {
	registry, err := remote.NewRegistry("")
	if err != nil {
		return nil
	}
	return registry
}

// expandRefsAgainstDefaultRemote expands bare convenience refs (e.g.
// "code-review-base#fragments/conduct") into canonical URLs against the
// configured default remote, so profiles store canonical identity while the CLI
// accepts the short input form. A scheme-qualified ref (canonical URL or
// ctxloom:local) is returned untouched; so is any ref when no default remote is
// configured or its URL is unknown — the downstream parser then accepts or
// rejects it. kind selects the item-type segment (bundles/profiles).
func expandRefsAgainstDefaultRemote(refs []string, kind remote.ItemType, registry *remote.Registry) []string {
	if len(refs) == 0 || registry == nil {
		return refs
	}
	def := registry.GetDefault()
	if def == "" {
		return refs
	}
	rem, err := registry.Get(def)
	if err != nil || rem.URL == "" {
		return refs
	}
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = remote.ResolveRefString(ref, rem.URL, "", kind)
	}
	return out
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

var (
	profileDefaultUnset     bool
	profileDefaultExclusive bool
)

var profileDefaultCmd = &cobra.Command{
	Use:   "default [name|ref]",
	Short: "Set, clear, or show the default profile(s)",
	Long: `Manage which profile(s) run/weave use when none is passed with -p.

The default set is a LIST: multiple defaults may coexist, and a default may be a
local profile name OR a remote reference. With no argument, prints the current
default set.

Examples:
  ctxloom profile default                 # show current default(s)
  ctxloom profile default go-developer    # add a default
  ctxloom profile default https://github.com/user/ctxloom@profiles/dev
  ctxloom profile default --unset go-developer
  ctxloom profile default --exclusive go-developer  # make it the ONLY default`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		w := iox.NewErrWriter(cmd.OutOrStdout())

		// No argument: report the current default set.
		if len(args) == 0 {
			if profileDefaultUnset {
				return fmt.Errorf("--unset requires a profile name or reference")
			}
			if profileDefaultExclusive {
				return fmt.Errorf("--exclusive requires a profile name or reference")
			}
			defaults := cfg.ExplicitDefaultProfiles()
			if len(defaults) == 0 {
				w.Println("No default profile set.")
				return w.Err()
			}
			w.Println("Default profile(s):")
			for _, d := range defaults {
				w.Printf("  - %s\n", d)
			}
			return w.Err()
		}

		name := args[0]
		if name == "help" {
			return cmd.Help()
		}

		res, err := operations.SetDefaultProfile(cmd.Context(), cfg, operations.SetDefaultProfileRequest{
			Name:      name,
			Unset:     profileDefaultUnset,
			Exclusive: profileDefaultExclusive,
		})
		if err != nil {
			return err
		}

		switch res.Status {
		case "added":
			w.Printf("Set %q as a default profile.\n", name)
		case "set":
			w.Printf("Set %q as the only default profile.\n", name)
		case "removed":
			w.Printf("Cleared %q from the default profiles.\n", name)
		default:
			switch {
			case profileDefaultUnset:
				w.Printf("%q was not a default profile.\n", name)
			case profileDefaultExclusive:
				w.Printf("%q is already the only default profile.\n", name)
			default:
				w.Printf("%q is already a default profile.\n", name)
			}
		}
		return w.Err()
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

		res, err := operations.GetProfile(cmd.Context(), cfg, operations.GetProfileRequest{Name: name})
		if err != nil {
			return fmt.Errorf("profile %q not found", name)
		}
		isDefault := cfg.Profiles.IsDefaultProfile(res.Name)
		return emit(cmd, profileDetailJSON{GetProfileResult: res, Default: isDefault}, func() error {
			return renderProfileShow(cmd.OutOrStdout(), res, isDefault)
		})
	},
}

// profileDetailJSON is the --format json shape for `profile show`: the declared
// profile config plus whether it is a configured default. Frontends (the VSCode
// profile composer) read this to render a profile's authored composition.
type profileDetailJSON struct {
	*operations.GetProfileResult
	Default bool `json:"default"`
}

// renderProfileShow writes the human-readable detail view of one profile
// to out. Each optional section (description, parents, bundles, tags,
// variables, exclude_*) is suppressed when empty. Extracted from
// profileShowCmd's RunE.
func renderProfileShow(out io.Writer, p *operations.GetProfileResult, isDefault bool) error {
	w := iox.NewErrWriter(out)
	w.Printf("Profile: %s\n", p.Name)
	w.Printf("Path: %s\n", p.Path)
	if p.Bundle != "" {
		w.Printf("Bundle: %s\n", p.Bundle)
	}
	if isDefault {
		w.Println("Default: yes")
	}
	if p.Description != "" {
		w.Printf("Description: %s\n", p.Description)
	}
	if p.LLM != "" {
		w.Printf("LLM: %s\n", p.LLM)
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
  ctxloom profile modify go-developer --add-parent https://github.com/user/ctxloom@profiles/developer
  ctxloom profile modify developer --add-bundle https://github.com/user/ctxloom@bundles/go-development
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

		// Expand bare convenience refs against the default remote so additions
		// are stored canonical; removals are expanded too so they match the
		// canonical form already on disk.
		registry := defaultRemoteRegistry()
		req := operations.UpdateProfileRequest{
			Name:                   name,
			AddParents:             expandRefsAgainstDefaultRemote(profileUpdateAddParents, remote.ItemTypeProfile, registry),
			RemoveParents:          expandRefsAgainstDefaultRemote(profileUpdateRemoveParents, remote.ItemTypeProfile, registry),
			AddBundles:             expandRefsAgainstDefaultRemote(profileUpdateAddBundles, remote.ItemTypeBundle, registry),
			RemoveBundles:          expandRefsAgainstDefaultRemote(profileUpdateRemoveBundles, remote.ItemTypeBundle, registry),
			AddExcludeFragments:    profileUpdateAddExcludeFragments,
			RemoveExcludeFragments: profileUpdateRemoveExcludeFragments,
			AddExcludeMCP:          profileUpdateAddExcludeMCP,
			RemoveExcludeMCP:       profileUpdateRemoveExcludeMCP,
		}
		if cmd.Flags().Changed("description") {
			d := profileUpdateDescription
			req.Description = &d
		}
		if cmd.Flags().Changed("llm") {
			// Validate a non-empty value the same way create/run do; an empty
			// value clears the preference and skips validation.
			if profileUpdateLLM != "" {
				if _, err := validateExplicitLLM(cfg, profileUpdateLLM); err != nil {
					return err
				}
			}
			l := profileUpdateLLM
			req.LLM = &l
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
	profileUpdateLLM                    string
	profileUpdateAddExcludeFragments    []string
	profileUpdateRemoveExcludeFragments []string
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

	profilePath, err := resolveProfilePathForPush(cmd.Context(), cfg, profileName)
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
	result, err := pm.Publish(cmd.Context(), profilePath, remoteName, opts)
	if err != nil {
		return err
	}

	printPublishResult(result)
	return nil
}

// resolveProfilePathForPush returns the on-disk path of the named profile,
// which the publish manager needs as its source. Routed through the operations
// read-path so the CLI no longer constructs a loader inline.
func resolveProfilePathForPush(ctx context.Context, cfg *config.Config, profileName string) (string, error) {
	res, err := operations.GetProfile(ctx, cfg, operations.GetProfileRequest{Name: profileName})
	if err != nil {
		return "", fmt.Errorf("profile not found: %s", profileName)
	}
	return res.Path, nil
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
	fmt.Printf("Commit: %s\n", shortSHA(result.SHA))
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

	res, err := operations.ExportProfile(cmd.Context(), cfg, operations.ExportProfileRequest{Name: name, DestDir: destDir})
	if err != nil {
		return err
	}

	fmt.Printf("Exported: %s -> %s\n", res.Source, res.Dest)
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

	res, err := operations.ImportProfile(cmd.Context(), cfg, operations.ImportProfileRequest{
		SourcePath: srcPath,
		Force:      profileImportForce,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Imported: %s -> %s\n", res.Source, res.Dest)
	return nil
}

func init() {
	rootCmd.AddCommand(profileCmd)

	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileCreateCmd)
	profileCmd.AddCommand(profileDeleteCmd)
	profileCmd.AddCommand(profileDefaultCmd)
	profileCmd.AddCommand(profileShowCmd)
	profileCmd.AddCommand(profileEditCmd)
	profileCmd.AddCommand(profileUpdateCmd)
	profileCmd.AddCommand(profilePushCmd)
	profileCmd.AddCommand(profileExportCmd)
	profileCmd.AddCommand(profileImportCmd)

	profileCreateCmd.Flags().StringSliceVar(&profileCreateParents, "parent", nil, "Parent profile URL(s) to inherit from")
	profileCreateCmd.Flags().StringSliceVarP(&profileCreateBundles, "bundle", "b", nil, "Bundle URL(s) to include")
	profileCreateCmd.Flags().StringVarP(&profileCreateDescription, "description", "d", "", "Description of the profile")
	profileCreateCmd.Flags().StringVar(&profileCreateLLM, "llm", "", "Preferred LLM config label/backend to launch (overridable by run -l)")

	profilePushCmd.Flags().BoolVar(&profilePushPR, "pr", false, "Create a pull request instead of pushing directly")
	profilePushCmd.Flags().StringVar(&profilePushBranch, "branch", "", "Target branch (default: repository default)")
	profilePushCmd.Flags().StringVarP(&profilePushMessage, "message", "m", "", "Commit message")

	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddParents, "add-parent", nil, "Parent profile URL(s) to add")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveParents, "remove-parent", nil, "Parent profile URL(s) to remove")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddBundles, "add-bundle", nil, "Bundle URL(s) to add")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveBundles, "remove-bundle", nil, "Bundle URL(s) to remove")
	profileUpdateCmd.Flags().StringVarP(&profileUpdateDescription, "description", "d", "", "New description for the profile")
	profileUpdateCmd.Flags().StringVar(&profileUpdateLLM, "llm", "", "Set the preferred LLM config label/backend (empty clears it)")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddExcludeFragments, "exclude-fragment", nil, "Fragment name(s) to exclude")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveExcludeFragments, "include-fragment", nil, "Fragment name(s) to stop excluding")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddExcludeMCP, "exclude-mcp", nil, "MCP server name(s) to exclude")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveExcludeMCP, "include-mcp", nil, "MCP server name(s) to stop excluding")

	profileImportCmd.Flags().BoolVarP(&profileImportForce, "force", "f", false, "Overwrite existing profile")

	profileDefaultCmd.Flags().BoolVar(&profileDefaultUnset, "unset", false, "Remove the named profile from the default set")
	profileDefaultCmd.Flags().BoolVar(&profileDefaultExclusive, "exclusive", false, "Make the named profile the SOLE default, unsetting all others")
	profileDefaultCmd.MarkFlagsMutuallyExclusive("unset", "exclusive")

	// Register positional arg completions
	profileShowCmd.ValidArgsFunction = completeProfileNames
	profileDeleteCmd.ValidArgsFunction = completeProfileNames
	profileDefaultCmd.ValidArgsFunction = completeProfileNames
	profileEditCmd.ValidArgsFunction = completeProfileNames
	profileUpdateCmd.ValidArgsFunction = completeProfileNames
	profilePushCmd.ValidArgsFunction = completeProfileNames
	profileExportCmd.ValidArgsFunction = completeProfileNames

	// Register flag completions
	_ = profileCreateCmd.RegisterFlagCompletionFunc("parent", completeProfileNames)
	_ = profileCreateCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
	_ = profileUpdateCmd.RegisterFlagCompletionFunc("add-parent", completeProfileNames)
	_ = profileUpdateCmd.RegisterFlagCompletionFunc("remove-parent", completeProfileNames)
	_ = profileUpdateCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
}
