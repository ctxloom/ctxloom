package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// Bare `ctxloom profile` lists the profiles: the collection is the one
// thing the noun is about, and reading it touches nothing.
var profileCmd = groupNodeDefault(&cobra.Command{
	Use:   "profile",
	Short: "Manage profiles (named fragment collections)",
	Long: `Manage profiles - named collections of context fragments, bundles, and configuration.

Profiles are stored as YAML files in .ctxloom/profiles/<name>.yaml and allow you to
quickly switch between different sets of context without specifying them individually.`,
}, "list")

var profileListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all profiles",
	RunE:    runProfileList,
}

func runProfileList(cmd *cobra.Command, args []string) error {
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
	profileDirs := profiles.GetProfileDirs(cfg.FS(), cfg.GetAppPaths())
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
	if shown, err := helpShortcut(cmd, name); shown {
		return err
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

	// Bundle/parent refs are canonicalized on store by operations.CreateProfile
	// (decision B: a per-remote short "<remote>/<bundle>[#profiles/...]" ref is
	// expanded to its canonical URL there, so the CLI and MCP paths share one
	// choke). A bare, unprefixed name is LOCAL (decision A) — no longer expanded
	// against a default remote.

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
		Loader:      profiles.NewLoader(profileCreateDirs(cfg), profileLoaderFSOptions(cfg)...),
	})
	if err != nil {
		return err
	}

	printProfileCreated(cmd.OutOrStdout(), name, res.Path)
	return nil
}

// profileCreateDirs returns the profile directories, defaulting to
// <appPath>/profiles when none are configured yet.
func profileCreateDirs(cfg *config.Config) []string {
	dirs := profiles.GetProfileDirs(cfg.FS(), cfg.GetAppPaths())
	if len(dirs) == 0 {
		// Mirror the len(AppPaths)==0 guard the rest of internal/config uses
		// before indexing AppPaths[0]; a directly-constructed Config can carry
		// an empty slice, which would otherwise panic here.
		appPaths := cfg.GetAppPaths()
		if len(appPaths) == 0 {
			return nil
		}
		dirs = []string{filepath.Join(appPaths[0], "profiles")}
	}
	return dirs
}

// profileLoaderFSOptions threads the config's filesystem into a profile loader
// so reads and WRITES land where the directories were discovered. Empty when
// the config carries no injected filesystem, which leaves the loader on its OS
// default.
func profileLoaderFSOptions(cfg *config.Config) []profiles.LoaderOption {
	if fs := cfg.FS(); fs != nil {
		return []profiles.LoaderOption{profiles.WithFS(fs)}
	}
	return nil
}

// printProfileCreated reports a newly-created profile's parents/bundles and path.
func printProfileCreated(w io.Writer, name, path string) {
	var parts []string
	if len(profileCreateParents) > 0 {
		parts = append(parts, fmt.Sprintf("parents: %s", strings.Join(profileCreateParents, ", ")))
	}
	if len(profileCreateBundles) > 0 {
		parts = append(parts, fmt.Sprintf("bundles: %s", strings.Join(profileCreateBundles, ", ")))
	}
	fmt.Fprintf(w, "Created profile %q with %s\n", name, strings.Join(parts, "; "))
	fmt.Fprintf(w, "Saved to: %s\n", path)
}

var profileRemoveYes bool

var profileRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm", "del"},
	Short:   "Remove a profile",
	Long: `Remove a profile.

Bare invocation reports what would be removed and removes nothing (exit 0).
Pass --yes to apply it.`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileRemove,
}

func runProfileRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	if shown, err := helpShortcut(cmd, name); shown {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	applyCmd := fmt.Sprintf("ctxloom profile remove %s --yes", name)
	if !profileRemoveYes {
		res, err := operations.GetProfile(cmd.Context(), cfg, operations.GetProfileRequest{Name: name})
		if err != nil {
			return fmt.Errorf("profile %q not found", name)
		}
		var detail []string
		if n := len(res.Bundles); n > 0 {
			detail = []string{fmt.Sprintf("%d bundle(s)", n)}
		}
		target := fmt.Sprintf("profile %q", name)
		return emit(cmd, newRemovePreviewResult(target, detail, applyCmd), func() error {
			printRemovePreview(cmd.OutOrStdout(), target, detail, applyCmd)
			return nil
		})
	}

	// Operations core deletes the profile AND clears it from the config
	// defaults if it was the default — a cleanup the old CLI path skipped.
	res, err := operations.DeleteProfile(cmd.Context(), cfg, operations.DeleteProfileRequest{Name: name})
	if err != nil {
		return err
	}

	return emit(cmd, res, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed profile %q\n", name)
		return nil
	})
}

var profileShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show details of a profile",
	Args:  cobra.ExactArgs(1),
	RunE:  runProfileShow,
}

func runProfileShow(cmd *cobra.Command, args []string) error {
	name := args[0]
	if shown, err := helpShortcut(cmd, name); shown {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	res, err := operations.GetProfile(cmd.Context(), cfg, operations.GetProfileRequest{Name: name})
	if err != nil {
		return fmt.Errorf("profile %q not found", name)
	}
	// "Default" now means membership in the default AGENT's composed profiles
	// (profiles.defaults was retired — see Config.DefaultAgentProfiles).
	isDefault := slices.Contains(cfg.DefaultAgentProfiles(), res.Name)
	return emit(cmd, profileDetailJSON{GetProfileResult: res, Default: isDefault}, func() error {
		return renderProfileShow(cmd.OutOrStdout(), res, isDefault)
	})
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
  ctxloom profile modify go-developer --add-parent 'https://github.com/user/ctxloom@bundles/dev#profiles/developer'
  ctxloom profile modify developer --add-bundle https://github.com/user/ctxloom@bundles/go-development
  ctxloom profile modify developer -d "New description"`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileUpdate,
}

func runProfileUpdate(cmd *cobra.Command, args []string) error {
	name := args[0]
	if shown, err := helpShortcut(cmd, name); shown {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Bundle/parent refs are canonicalized on store by operations.UpdateProfile
	// (decision B): a short "<remote>/<bundle>[#profiles/...]" ref expands to its
	// canonical URL, and removals canonicalize the same way so they match the
	// on-disk form. A bare, unprefixed name is LOCAL (decision A).
	req := operations.UpdateProfileRequest{
		Name:                   name,
		AddParents:             profileUpdateAddParents,
		RemoveParents:          profileUpdateRemoveParents,
		AddBundles:             profileUpdateAddBundles,
		RemoveBundles:          profileUpdateRemoveBundles,
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

var profileEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit a profile",
	Long: `Edit a profile's YAML file using your configured editor.

Examples:
  ctxloom profile edit my-profile`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileEdit,
}

func runProfileEdit(cmd *cobra.Command, args []string) error {
	return editProfileFile(cmd.OutOrStdout(), args[0])
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

	fmt.Fprintf(cmd.OutOrStdout(), "Exported: %s -> %s\n", res.Source, res.Dest)
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

	fmt.Fprintf(cmd.OutOrStdout(), "Imported: %s -> %s\n", res.Source, res.Dest)
	return nil
}

func init() {
	rootCmd.AddCommand(profileCmd)

	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileCreateCmd)
	profileCmd.AddCommand(profileRemoveCmd)
	profileCmd.AddCommand(profileShowCmd)
	profileCmd.AddCommand(profileEditCmd)
	profileCmd.AddCommand(profileUpdateCmd)
	profileCmd.AddCommand(profileExportCmd)
	profileCmd.AddCommand(profileImportCmd)

	profileCreateCmd.Flags().StringSliceVar(&profileCreateParents, "parent", nil, "Parent profile(s) to inherit from: a local name or <bundle>#profiles/<name> (bundle = canonical URL, remote/bundle alias, or local bundle name)")
	profileCreateCmd.Flags().StringSliceVarP(&profileCreateBundles, "bundle", "b", nil, "Bundle URL(s) to include")
	profileCreateCmd.Flags().StringVarP(&profileCreateDescription, "description", "d", "", "Description of the profile")
	profileCreateCmd.Flags().StringVar(&profileCreateLLM, "llm", "", "Preferred LLM config label/backend to launch (overridable by run -l)")

	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddParents, "add-parent", nil, "Parent profile(s) to add: a local name or <bundle>#profiles/<name> (bundle = canonical URL, remote/bundle alias, or local bundle name)")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveParents, "remove-parent", nil, "Parent profile(s) to remove")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddBundles, "add-bundle", nil, "Bundle URL(s) to add")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveBundles, "remove-bundle", nil, "Bundle URL(s) to remove")
	profileUpdateCmd.Flags().StringVarP(&profileUpdateDescription, "description", "d", "", "New description for the profile")
	profileUpdateCmd.Flags().StringVar(&profileUpdateLLM, "llm", "", "Set the preferred LLM config label/backend (empty clears it)")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddExcludeFragments, "exclude-fragment", nil, "Fragment name(s) to exclude")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveExcludeFragments, "include-fragment", nil, "Fragment name(s) to stop excluding")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddExcludeMCP, "exclude-mcp", nil, "MCP server name(s) to exclude")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveExcludeMCP, "include-mcp", nil, "MCP server name(s) to stop excluding")

	profileImportCmd.Flags().BoolVarP(&profileImportForce, "force", "f", false, "Overwrite existing profile")
	profileRemoveCmd.Flags().BoolVarP(&profileRemoveYes, "yes", "y", false, "Apply the removal this invocation would report (default: report only)")

	// Register positional arg completions
	profileShowCmd.ValidArgsFunction = completeProfileNames
	profileRemoveCmd.ValidArgsFunction = completeProfileNames
	profileEditCmd.ValidArgsFunction = completeProfileNames
	profileUpdateCmd.ValidArgsFunction = completeProfileNames
	profileExportCmd.ValidArgsFunction = completeProfileNames

	// Register flag completions
	_ = profileCreateCmd.RegisterFlagCompletionFunc("parent", completeProfileNames)
	_ = profileCreateCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
	_ = profileUpdateCmd.RegisterFlagCompletionFunc("add-parent", completeProfileNames)
	_ = profileUpdateCmd.RegisterFlagCompletionFunc("remove-parent", completeProfileNames)
	_ = profileUpdateCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
}
