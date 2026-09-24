package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// This file is Part B6a's `ctxloom skill` CLI group — the true Agent Skills
// surface (SKILL.md directory packages), mirroring command_cmd.go's
// structure (subcommand-per-file-in-spirit, cobra group, bundle resolution,
// `emit`-based text/json output) but calling the dedicated operations/skills.go
// core instead of the fragment/command ItemType machinery: a skill is a
// directory tree, not a single text blob, so it does not fit that shape.

// Bare `ctxloom skill` lists the skills: the collection is the one thing
// the noun is about, and reading it touches nothing.
var skillCmd = groupNodeDefault(&cobra.Command{
	Use:   "skill",
	Short: "Manage Agent Skills (SKILL.md packages)",
	Long: `Manage Agent Skills — model-invoked SKILL.md packages (instructions plus
optional scripts/assets) that an engine loads via progressive disclosure,
distinct from a user-invoked slash "command" (ctxloom command).

Skills live inside bundles — .ctxloom/content/bundles/v2/<bundle>/skills/<name>/
— and are referenced using the syntax: bundle#skills/name

Examples:
  ctxloom skill list                                   # List all skills
  ctxloom skill show core#skills/code-reviewer          # Show frontmatter + files
  ctxloom skill create my-bundle code-reviewer          # Scaffold a new skill package
  ctxloom skill remove my-bundle#skills/code-reviewer --yes  # Remove a skill package
  ctxloom skill export my-bundle#skills/code-reviewer    # Pack to an Anthropic-shaped .zip
  ctxloom skill import ./code-reviewer.zip --bundle my-bundle`,
}, "list")

var skillListBundle string

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all Agent Skill packages",
	Long: `List all Agent Skill packages from all installed bundles.

Use --bundle to filter by a specific bundle.`,
	RunE: runSkillList,
}

func runSkillList(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	res, err := operations.ListSkills(cmd.Context(), cfg, operations.ListSkillsRequest{SortBy: "source"})
	if err != nil {
		return fmt.Errorf("failed to list skills: %w", err)
	}
	entries := res.Skills
	totalCount := len(entries)
	if skillListBundle != "" {
		filtered := make([]operations.SkillEntry, 0, len(entries))
		for _, e := range entries {
			if e.Source == skillListBundle {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	return emit(cmd, entries, func() error {
		return printSkillList(cmd, entries, skillListBundle, totalCount)
	})
}

// printSkillList renders the skill listing. When --bundle filtered every
// result out, bundleFilter/totalCount let it distinguish "no skills exist
// anywhere" (the create-one hint applies) from "none matched --bundle X, but
// skills do exist in other bundles" (the unqualified "No skills
// found" message used to claim the former even when the latter was true).
func printSkillList(cmd *cobra.Command, entries []operations.SkillEntry, bundleFilter string, totalCount int) error {
	out := cmd.OutOrStdout()
	if len(entries) == 0 {
		if bundleFilter != "" && totalCount > 0 {
			fmt.Fprintf(out, "No skills found in bundle %q.\n", bundleFilter)
			fmt.Fprintln(out, "Skills exist in other bundles — run `ctxloom skill list` without --bundle to see them all.")
			return nil
		}
		fmt.Fprintln(out, "No skills found.")
		fmt.Fprintln(out, "Create one with: ctxloom skill create <bundle> <name>")
		return nil
	}
	fmt.Fprintf(out, "Skills (%d):\n\n", len(entries))
	currentBundle := ""
	for _, e := range entries {
		if e.Source != currentBundle {
			if currentBundle != "" {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "  %s:\n", e.Source)
			currentBundle = e.Source
		}
		fmt.Fprintf(out, "    - %s", e.Name)
		if e.Description != "" {
			fmt.Fprintf(out, ": %s", e.Description)
		}
		fmt.Fprintf(out, " [%d file(s)]", e.FileCount)
		if len(e.Tags) > 0 {
			fmt.Fprintf(out, " (%s)", strings.Join(e.Tags, ", "))
		}
		fmt.Fprintln(out)
	}
	return nil
}

var skillShowCmd = &cobra.Command{
	Use:   "show <bundle>#skills/<name>",
	Short: "Show a skill's frontmatter and file manifest",
	Long: `Display a skill's SKILL.md frontmatter (description, license, etc.),
instructions body, and per-file manifest (path, sha256, mode).

Reference format: bundle#skills/name

Examples:
  ctxloom skill show core#skills/code-reviewer`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillShow,
}

func runSkillShow(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	res, err := operations.GetSkill(cmd.Context(), cfg, operations.GetSkillRequest{Name: args[0]})
	if err != nil {
		return err
	}
	return emit(cmd, res, func() error {
		return renderSkillShow(cmd.OutOrStdout(), res)
	})
}

// renderSkillShow is `skill show`'s text rendering.
func renderSkillShow(out io.Writer, res *operations.GetSkillResult) error {
	fmt.Fprintf(out, "%s#skills/%s\n\n", res.Bundle, res.Name)
	fmt.Fprintf(out, "description: %s\n", res.Description)
	if res.License != "" {
		fmt.Fprintf(out, "license: %s\n", res.License)
	}
	if res.Compatibility != "" {
		fmt.Fprintf(out, "compatibility: %s\n", res.Compatibility)
	}
	if len(res.AllowedTools) > 0 {
		fmt.Fprintf(out, "allowed-tools: %s\n", strings.Join(res.AllowedTools, ", "))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, res.Body)
	fmt.Fprintf(out, "\nFiles (%d):\n", len(res.Files))
	for _, f := range res.Files {
		fmt.Fprintf(out, "  %s  %s  %s\n", f.Mode, f.SHA256, f.Path)
	}
	return nil
}

var skillCreateDescription string

var skillCreateCmd = &cobra.Command{
	Use:   "create <bundle> <name>",
	Short: "Scaffold a new Agent Skill package",
	Long: `Scaffold a new Agent Skill package (skills/<name>/SKILL.md) in an existing
bundle. The directory is the skill: nothing else is registered.

The scaffolded SKILL.md has valid frontmatter (name matching the directory,
a placeholder description) that passes validation immediately — edit
SKILL.md to describe the skill and add any scripts/assets.

Examples:
  ctxloom skill create my-bundle code-reviewer
  ctxloom skill create my-bundle code-reviewer --description "Reviews Go diffs for common bugs"`,
	Args: cobra.ExactArgs(2),
	RunE: runSkillCreate,
}

func runSkillCreate(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	res, err := operations.CreateSkill(cmd.Context(), cfg, operations.CreateSkillRequest{
		Bundle:      args[0],
		Name:        args[1],
		Description: skillCreateDescription,
	})
	if err != nil {
		return err
	}
	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Created skill %q in bundle %q\n", res.Name, res.Bundle)
		fmt.Fprintf(out, "  %s\n", res.Dir)
		fmt.Fprintf(out, "Edit %s/SKILL.md to describe the skill\n", res.Dir)
		return nil
	})
}

var skillRemoveYes bool

var skillRemoveCmd = &cobra.Command{
	Use:     "remove <bundle>#skills/<name>",
	Aliases: []string{"rm", "del"},
	Short:   "Remove an Agent Skill package",
	Long: `Remove a skill package: its directory (skills/<name>/).

Bare invocation reports what would be removed and removes nothing (exit 0).
Pass --yes to apply it.

Reference format: bundle#skills/name

Examples:
  ctxloom skill remove my-bundle#skills/old-skill
  ctxloom skill remove my-bundle#skills/old-skill --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillRemove,
}

func runSkillRemove(cmd *cobra.Command, args []string) error {
	bundleName, skillName, ok := strings.Cut(args[0], "#skills/")
	if !ok {
		return fmt.Errorf("invalid skill reference: expected bundle#skills/name (got %q)", args[0])
	}
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	applyCmd := fmt.Sprintf("ctxloom skill remove %s --yes", args[0])
	if !skillRemoveYes {
		// Confirm the skill exists before reporting: a preview naming a
		// target that isn't there would be worse than the not-found error.
		// Deliberately the UNGATED listing (ListSkills, the same one `skill
		// list` uses) rather than GetSkill's content-validating read: a
		// skill whose SKILL.md is malformed or withheld must still be
		// removable — this is a membership check, not a well-formedness one.
		if !skillExistsInBundle(cmd.Context(), cfg, bundleName, skillName) {
			return fmt.Errorf("skill %q not found in bundle %q", skillName, bundleName)
		}
		target := fmt.Sprintf("skill %q from bundle %q", skillName, bundleName)
		return emit(cmd, newRemovePreviewResult(target, nil, applyCmd), func() error {
			printRemovePreview(cmd.OutOrStdout(), target, nil, applyCmd)
			return nil
		})
	}

	res, err := operations.RemoveSkill(cmd.Context(), cfg, operations.RemoveSkillRequest{Bundle: bundleName, Name: skillName})
	if err != nil {
		return err
	}
	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Removed skill %q from bundle %q\n", res.Name, res.Bundle)
		fmt.Fprintf(out, "  %s\n", res.Dir)
		return nil
	})
}

// skillExistsInBundle reports whether bundle holds a skill named name — a
// membership check via the same ungated ListSkills path `skill list` uses, degrading to false (not found) rather than propagating a
// listing error, since a listing failure and "this one skill is absent" are
// both refused identically by runSkillRemove's caller.
func skillExistsInBundle(ctx context.Context, cfg *config.Config, bundle, name string) bool {
	res, err := operations.ListSkills(ctx, cfg, operations.ListSkillsRequest{})
	if err != nil {
		return false
	}
	for _, e := range res.Skills {
		// SkillEntry.Name is sometimes the bare name and sometimes
		// "<bundle>/<name>" depending on how the loader disambiguated it —
		// match either spelling rather than depending on which one the
		// listing path happens to produce.
		if e.Source == bundle && (e.Name == name || e.Name == bundle+"/"+name) {
			return true
		}
	}
	return false
}

var skillExportOut string
var skillExportSign bool

var skillExportCmd = &cobra.Command{
	Use:   "export <bundle>#skills/<name>",
	Short: "Pack a skill to an Anthropic-shaped .zip",
	Long: `Pack a skill's source tree into a .zip shaped exactly like the Anthropic
Skills-API upload expects: a single top-level directory (named after the
skill) containing every manifest file, with POSIX modes preserved.

--sign additionally writes a detached signature over the skill's manifest
(the same bytes 'ctxloom skill import' verifies), using the same zero-config
key discovery 'ctxloom bundle sign' uses.

Examples:
  ctxloom skill export my-bundle#skills/code-reviewer
  ctxloom skill export my-bundle#skills/code-reviewer -o /tmp/code-reviewer.zip --sign`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillExport,
}

func runSkillExport(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	bundleName, skillName, ok := strings.Cut(args[0], "#skills/")
	if !ok {
		return fmt.Errorf("invalid skill reference: expected bundle#skills/name (got %q)", args[0])
	}
	req := operations.ExportSkillRequest{
		Bundle:  bundleName,
		Name:    skillName,
		OutPath: skillExportOut,
		Sign:    skillExportSign,
	}
	if skillExportSign {
		discoverer, err := operations.SignerDiscoverer()
		if err != nil {
			return err
		}
		discovered, err := discoverer.Discover(cmd.Context(), cfg.SignKey())
		if err != nil {
			return err
		}
		defer func() { _ = discovered.Close() }()
		req.Signer = discovered.Signer
	}
	res, err := operations.ExportSkill(cmd.Context(), cfg, req)
	if err != nil {
		return err
	}
	return emit(cmd, res, func() error {
		renderSkillExport(cmd.OutOrStdout(), res)
		return nil
	})
}

// renderSkillExport is `skill export`'s text rendering.
func renderSkillExport(out io.Writer, res *operations.ExportSkillResult) {
	fmt.Fprintf(out, "Exported %s -> %s (%d bytes)\n", res.Name, res.ZipPath, res.Bytes)
	if res.SigPath != "" {
		fmt.Fprintf(out, "Signed: %s\n", res.SigPath)
	}
}

var skillImportBundle string
var skillImportSig string

var skillImportCmd = &cobra.Command{
	Use:   "import <archive>",
	Short: "Import a skill archive (.zip/.tar.gz) into a bundle",
	Long: `Import a skill archive into a bundle via the hardened extractor: path
traversal, symlinks, hardlinks/device files, entry-count bombs, and
decompression bombs are all rejected before anything is written to disk.
Accepts either the canonical Anthropic-shaped .zip or a .tar.gz.

The imported tree lands as REVIEWABLE content — pending review like any
freshly-pulled remote bundle content, never auto-trusted. If --sig names a
detached signature (as 'ctxloom skill export --sign' produces), it is
verified against the extracted tree's own recomputed manifest before the
import is reported; an unsigned or untrusted-publisher signature does not
block the import (ctxloom never auto-trusts remote content on import —
'ctxloom review'/'ctxloom trust' still govern whether it is ever exposed),
but a STRUCTURALLY invalid archive or package (a rejected entry, or a
SKILL.md that fails frontmatter validation) is refused and cleaned up.

Examples:
  ctxloom skill import ./code-reviewer.zip --bundle my-bundle
  ctxloom skill import ./code-reviewer.zip --bundle my-bundle --sig ./code-reviewer.zip.sig`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillImport,
}

func runSkillImport(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if skillImportBundle == "" {
		return fmt.Errorf("--bundle is required")
	}
	res, err := operations.ImportSkill(cmd.Context(), cfg, operations.ImportSkillRequest{
		Bundle:      skillImportBundle,
		ArchivePath: args[0],
		SigPath:     skillImportSig,
	})
	if err != nil {
		return err
	}
	return emit(cmd, res, func() error {
		renderSkillImport(cmd.OutOrStdout(), res)
		return nil
	})
}

// renderSkillImport is `skill import`'s text rendering.
func renderSkillImport(out io.Writer, res *operations.ImportSkillResult) {
	fmt.Fprintf(out, "Imported skill %q into bundle %q (%d file(s))\n", res.Name, res.Bundle, res.FileCount)
	fmt.Fprintf(out, "  %s\n", res.Dir)
	fmt.Fprintf(out, "  signature: %s\n", res.SignatureState)
	// The bundle name is NOT appended as an argument: reviewCmd is
	// cobra.NoArgs, so `ctxloom review <bundle>` — which this line used to
	// print — is rejected by the very tool that advised it. The bundle is
	// already named on the first line of this output; what the user needs
	// here is a command that runs.
	fmt.Fprintln(out, "Pending review — run: ctxloom review")
}

func init() {
	rootCmd.AddCommand(skillCmd)

	skillCmd.AddCommand(skillListCmd)
	skillCmd.AddCommand(skillShowCmd)
	skillCmd.AddCommand(skillCreateCmd)
	skillCmd.AddCommand(skillRemoveCmd)
	skillCmd.AddCommand(skillExportCmd)
	skillCmd.AddCommand(skillImportCmd)

	skillListCmd.Flags().StringVarP(&skillListBundle, "bundle", "b", "", "Filter by bundle name")
	skillCreateCmd.Flags().StringVarP(&skillCreateDescription, "description", "d", "", "SKILL.md frontmatter description (default: a TODO placeholder)")
	skillRemoveCmd.Flags().BoolVarP(&skillRemoveYes, "yes", "y", false, "Apply the removal this invocation would report (default: report only)")
	skillExportCmd.Flags().StringVarP(&skillExportOut, "out", "o", "", "Output .zip path (default: <name>.zip)")
	skillExportCmd.Flags().BoolVar(&skillExportSign, "sign", false, "sign the exported manifest (writes a detached .sig sibling)")
	skillImportCmd.Flags().StringVar(&skillImportBundle, "bundle", "", "target bundle to import into (required)")
	skillImportCmd.Flags().StringVar(&skillImportSig, "sig", "", "path to a detached signature covering the archive's manifest")
}
