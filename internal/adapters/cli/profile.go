package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
)

// Bare `ctxloom profile` lists the profiles: the collection is the one
// thing the noun is about, and reading it touches nothing.
var profileCmd = groupNodeDefault(&cobra.Command{
	Use:   "profile",
	Short: "Manage profiles (named fragment collections)",
	Long: `Manage profiles - named collections of context fragments, bundles, and configuration.

A profile is an item of a bundle. A project's own profiles live in its
project bundle, so a bare profile name is that bundle's profile; a profile of
any other bundle is addressed as <bundle>#profiles/<name>. The write commands
(create, modify, edit, remove, import) write into the project bundle unless
the name addresses another LOCAL bundle (create and import take --bundle).`,
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

	res, err := operations.ListProfiles(cmd.Context(), cfg, operations.ListProfilesRequest{})
	if err != nil {
		return err
	}
	list := res.Profiles
	if list == nil {
		list = []operations.ProfileEntry{}
	}

	return emit(cmd, list, func() error {
		out := cmd.OutOrStdout()
		if len(list) == 0 {
			fmt.Fprintln(out, "No profiles defined.")
			if hint := emptyListingHint(cfg); hint == noProjectListingHint {
				fmt.Fprintln(out, hint)
			} else {
				fmt.Fprintln(out, "Use 'ctxloom profile create <name> --include <bundle>...' to create one.")
			}
			return nil
		}
		var refs []string
		for _, p := range list {
			refs = append(append(refs, p.Name, p.Bundle), p.Parents...)
		}
		return renderProfileList(out, list, refLabels(refLabeler(cmd.Context(), cfg), refs...))
	})
}

// renderProfileList writes the human-readable summary of a profile list
// to out. Extracted from profileListCmd's RunE so the formatting decisions
// (default-tag, parents/bundles line, description indentation) are
// testable without invoking cobra or touching the real config. The
// per-entry Default flag is resolved by the operations layer.
func renderProfileList(out io.Writer, list []operations.ProfileEntry, show func(string) string) error {
	w := errwriter.New(out)
	w.Printf("Profiles (%d):\n", len(list))
	for _, p := range list {
		w.Printf("  %s", show(p.Name))
		if p.Default {
			w.Printf(" (default)")
		}
		w.Println()
		if p.Description != "" {
			w.Printf("    %s\n", p.Description)
		}

		var parts []string
		if p.Bundle != "" {
			parts = append(parts, fmt.Sprintf("from bundle: %s", show(p.Bundle)))
		}
		if len(p.Parents) > 0 {
			parents := make([]string, len(p.Parents))
			for i, parent := range p.Parents {
				parents[i] = show(parent)
			}
			parts = append(parts, fmt.Sprintf("parents: %s", strings.Join(parents, ", ")))
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
	profileCreateIncludes    []string
	profileCreateDescription string
	profileCreateLLM         string
	profileCreateTarget      string
)

var profileCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new profile",
	Long: `Create a new profile with included bundles and/or parents.

--include names a bundle the profile pulls in; --bundle names the LOCAL bundle
the profile is written into (default: the project bundle).

Included bundle references use full URLs:
  https://github.com/user/repo@bundles/name    # Bundle from remote

Example:
  ctxloom profile create developer -i https://github.com/user/ctxloom@bundles/go-development -d "Standard dev context"`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileCreate,
}

func runProfileCreate(cmd *cobra.Command, args []string) error {
	name := args[0]

	if len(profileCreateParents) == 0 && len(profileCreateIncludes) == 0 {
		return fmt.Errorf("at least one parent (--parent) or included bundle (--include) is required")
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
	// validation and its choice of the local bundle the profile lands in.
	res, err := operations.CreateProfile(cmd.Context(), cfg, operations.CreateProfileRequest{
		Name:        inBundle(profileCreateTarget, name),
		Description: profileCreateDescription,
		LLM:         profileCreateLLM,
		Parents:     profileCreateParents,
		Bundles:     profileCreateIncludes,
	})
	if err != nil {
		return err
	}

	printProfileCreated(cmd.OutOrStdout(), name, res.Path)
	return nil
}

// inBundle is the name a profile called name is addressed by in the LOCAL
// bundle called bundle: the name itself for the project bundle (an empty
// bundle), the "<bundle>#profiles/<name>" ref for any other.
func inBundle(bundle, name string) string {
	if bundle == "" {
		return name
	}
	return bundle + refuri.ProfileSelector + name
}

// printProfileCreated reports a newly-created profile's parents/bundles and path.
func printProfileCreated(w io.Writer, name, path string) {
	var parts []string
	if len(profileCreateParents) > 0 {
		parts = append(parts, fmt.Sprintf("parents: %s", strings.Join(profileCreateParents, ", ")))
	}
	if len(profileCreateIncludes) > 0 {
		parts = append(parts, fmt.Sprintf("bundles: %s", strings.Join(profileCreateIncludes, ", ")))
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

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	applyCmd := fmt.Sprintf("ctxloom profile remove %s --yes", name)
	if !profileRemoveYes {
		res, err := operations.GetProfile(cmd.Context(), cfg, operations.GetProfileRequest{Name: name})
		if err != nil {
			if shown, herr := helpFallback(cmd, name); shown {
				return herr
			}
			return err
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
		if shown, herr := helpFallback(cmd, name); shown {
			return herr
		}
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

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	res, err := operations.GetProfile(cmd.Context(), cfg, operations.GetProfileRequest{Name: name})
	if err != nil {
		if shown, herr := helpFallback(cmd, name); shown {
			return herr
		}
		return err
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
//
// A profile can ship inside a pulled bundle, so every value but Path (the
// local file) is publisher-authored and goes through inertField.
func renderProfileShow(out io.Writer, p *operations.GetProfileResult, isDefault bool) error {
	w := errwriter.New(out)
	w.Printf("Profile: %s\n", inertField(p.Name))
	w.Printf("Path: %s\n", p.Path)
	if p.Bundle != "" {
		w.Printf("Bundle: %s\n", inertField(p.Bundle))
	}
	if isDefault {
		w.Println("Default: yes")
	}
	if p.Description != "" {
		w.Printf("Description: %s\n", inertField(p.Description))
	}
	if p.LLM != "" {
		w.Printf("LLM: %s\n", inertField(p.LLM))
	}
	writeBulletList(w, "Parents", p.Parents)
	writeBulletList(w, "Bundles", p.Bundles)
	writeBulletList(w, "Tags", p.Tags)
	if len(p.Variables) > 0 {
		w.Println("Variables:")
		for k, v := range p.Variables {
			w.Printf("  %s: %s\n", inertField(k), inertField(v))
		}
	}
	writeBulletList(w, "Excluded fragments", p.ExcludeFragments)
	writeBulletList(w, "Excluded MCP servers", p.ExcludeMCP)
	return w.Err()
}

func writeBulletList(w *errwriter.Writer, heading string, items []string) {
	if len(items) == 0 {
		return
	}
	w.Printf("%s:\n", heading)
	for _, item := range items {
		w.Printf("  - %s\n", inertField(item))
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
		if shown, herr := helpFallback(cmd, name); shown {
			return herr
		}
		return err
	}

	w := errwriter.New(cmd.OutOrStdout())
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
	Long: `Export a local bundle's profile to an arbitrary directory.

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

var (
	profileImportForce  bool
	profileImportTarget string
)

var profileImportCmd = &cobra.Command{
	Use:   "import <path>",
	Short: "Import a profile from a local file",
	Long: `Import a profile YAML file into the project bundle (or, with --bundle,
another local bundle) as the profile named by the file's basename.

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
		Bundle:     profileImportTarget,
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
	profileCreateCmd.Flags().StringSliceVarP(&profileCreateIncludes, "include", "i", nil, "Bundle URL(s) the profile includes")
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
	profileImportCmd.Flags().StringVar(&profileImportTarget, "bundle", "", "Local bundle to import into (default: the project bundle)")
	profileCreateCmd.Flags().StringVar(&profileCreateTarget, "bundle", "", "Local bundle to create the profile in (default: the project bundle)")
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
