package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file is the operations core for `ctxloom skill`: author (create,
// remove), inspect (list/show), and interchange (export/import) an Agent Skill
// package. It plays the same frontend-agnostic
// role for skills that items.go plays for fragments/commands, but a skill is a
// DIRECTORY TREE, not a single text blob, so it gets its own focused
// request/result shapes rather than joining the ItemKind machinery (mirrors
// how MCP servers/hooks already get their own focused operations rather than
// forcing the fragment/command shape).

// SkillEntry represents an Agent Skill package in listing results — the skill
// analog of CommandEntry. FileCount stands in for "content": a skill has no
// single blob, only a file tree.
type SkillEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Source      string   `json:"source"`
	FileCount   int      `json:"file_count"`
}

// ListSkillsRequest contains parameters for listing skills.
type ListSkillsRequest struct {
	Query  string `json:"query"`
	SortBy string `json:"sort_by"` // "name" or "source"

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// ListSkillsResult contains the list of skills.
type ListSkillsResult struct {
	Skills []SkillEntry `json:"skills"`
	Count  int          `json:"count"`
}

// ListSkills returns all Agent Skill packages matching the criteria. Mirrors
// ListCommands: the management/listing read path (ungated bundleLoader) so a
// pending-review skill still shows up to be reviewed.
func ListSkills(_ context.Context, cfg *config.Config, req ListSkillsRequest) (*ListSkillsResult, error) {
	loader := req.Loader
	if loader == nil {
		if cfg == nil {
			return nil, fmt.Errorf("no .ctxloom directory configured")
		}
		loader = bundleLoader(cfg)
	}
	infos, err := loader.ListAllSkills()
	if err != nil {
		return nil, err
	}

	if req.Query != "" {
		query := strings.ToLower(req.Query)
		var filtered []bundles.SkillInfo
		for _, info := range infos {
			if strings.Contains(strings.ToLower(info.Name), query) ||
				strings.Contains(strings.ToLower(info.Description), query) {
				filtered = append(filtered, info)
			}
		}
		infos = filtered
	}

	sort.Slice(infos, func(i, j int) bool {
		if req.SortBy == "source" && infos[i].Bundle != infos[j].Bundle {
			return infos[i].Bundle < infos[j].Bundle
		}
		return infos[i].Name < infos[j].Name
	})

	result := &ListSkillsResult{Skills: make([]SkillEntry, 0, len(infos)), Count: len(infos)}
	for _, info := range infos {
		result.Skills = append(result.Skills, SkillEntry{
			Name:        info.Name,
			Description: info.Description,
			Tags:        info.Tags,
			Source:      info.Bundle,
			FileCount:   info.FileCount,
		})
	}
	return result, nil
}

// SkillFileEntry is one file's manifest entry in a GetSkillResult — the
// read-facing projection of bundles.SkillManifestEntry.
type SkillFileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

// GetSkillRequest contains parameters for reading one skill.
type GetSkillRequest struct {
	Name string `json:"name"`

	// Pipeline is an optional pre-configured process stage (for testing) —
	// see GetFragmentRequest.Pipeline.
	Pipeline *bundles.Pipeline `json:"-"`
}

// GetSkillResult carries a skill's frontmatter, instructions body, and
// per-file manifest.
type GetSkillResult struct {
	Name          string           `json:"name"`
	Bundle        string           `json:"bundle"`
	Description   string           `json:"description"`
	License       string           `json:"license,omitempty"`
	Compatibility string           `json:"compatibility,omitempty"`
	AllowedTools  []string         `json:"allowed_tools,omitempty"`
	Body          string           `json:"body"`
	Files         []SkillFileEntry `json:"files"`
}

// GetSkill returns a specific skill's frontmatter/body/manifest by name. Uses
// the gated exposure loader (like GetCommand) — the same surface
// `ctxloom://skills/{name}` and `ctxloom skill show` share, so a
// trust-withheld skill is not shown.
func GetSkill(_ context.Context, cfg *config.Config, req GetSkillRequest) (*GetSkillResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	pipe := req.Pipeline
	if pipe == nil {
		if cfg == nil {
			return nil, fmt.Errorf("no .ctxloom directory configured")
		}
		pipe = exposurePipeline(cfg)
	}
	ls, err := pipe.GetSkill(req.Name)
	if err != nil {
		return nil, err
	}
	files := make([]SkillFileEntry, 0, len(ls.Files))
	for _, f := range ls.Files {
		files = append(files, SkillFileEntry{
			Path:   f.RelPath,
			SHA256: bundles.HashPayload(f.Content),
			Mode:   fmt.Sprintf("%04o", f.Mode),
		})
	}
	return &GetSkillResult{
		Name:          ls.Item,
		Bundle:        ls.Bundle,
		Description:   ls.Frontmatter.Description,
		License:       ls.Frontmatter.License,
		Compatibility: ls.Frontmatter.Compatibility,
		AllowedTools:  ls.Frontmatter.AllowedTools,
		Body:          ls.Body,
		Files:         files,
	}, nil
}

// CreateSkillRequest is the input for CreateSkill.
type CreateSkillRequest struct {
	Bundle      string `json:"bundle"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"` // empty gets a TODO placeholder

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
	// FS, when non-nil, is the afero filesystem the skill directory is
	// written to; nil defaults to the OS filesystem.
	FS afero.Fs `json:"-"`
}

// CreateSkillResult reports a scaffolded skill package.
type CreateSkillResult struct {
	Status string `json:"status"`
	Bundle string `json:"bundle"`
	Name   string `json:"name"`
	Dir    string `json:"dir"`
}

// skillTemplate renders the scaffolded SKILL.md a fresh `ctxloom skill
// create` writes. name is both the frontmatter `name` (the directory name —
// one identity, so the emitted package lands where its frontmatter says) and
// the heading; description is a TODO the author must replace before the
// skill is useful (a placeholder description does not block create, but IS a
// signal for review to flag — left to the human, never silently filled in with
// something untrue).
//
// The frontmatter is built via yaml.Marshal (not a hand-rolled fmt template):
// a placeholder or author-supplied description containing a colon+space (a
// perfectly ordinary sentence, e.g. "TODO: describe...") breaks a plain YAML
// scalar built by naive string interpolation — yaml.Marshal quotes/escapes
// whatever the value needs, so this is correct for ANY description text, not
// just the shipped placeholder.
func skillTemplate(name, description string) string {
	fm := bundles.SkillFrontmatter{Name: name, Description: description}
	data, err := yaml.Marshal(fm)
	if err != nil {
		// SkillFrontmatter holds only strings/slices/maps, so yaml.Marshal
		// cannot fail on it — genuinely unreachable. A fallback here used to
		// rebuild the frontmatter via naive fmt.Sprintf string interpolation
		// — exactly the injection this function's own doc comment says
		// yaml.Marshal exists to prevent, for zero benefit since the branch
		// could never run. Panic loudly instead of silently reintroducing the
		// bug it would be covering for.
		panic(fmt.Sprintf("skillTemplate: yaml.Marshal of SkillFrontmatter failed unexpectedly: %v", err))
	}
	return "---\n" + string(data) + "---\n\n# " + name + "\n\nTODO: describe what this skill does and how to use it.\n"
}

// CreateSkill scaffolds a new Agent Skill package directory (SKILL.md with
// valid frontmatter) at skills/<name>/ inside an EXISTING bundle's tree. The
// directory IS the skill: a tree's items are found by walking it, so nothing
// is registered in bundle.yaml. The scaffold is validated with
// ParseSkillPackage before returning — a template that wouldn't itself pass
// validation is never left on disk claiming success.
func CreateSkill(_ context.Context, cfg *config.Config, req CreateSkillRequest) (*CreateSkillResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	bundle, err := loadBundleForUpdate(bundleStore(cfg, req.Store), cfg, req.Bundle)
	if err != nil {
		return nil, err
	}
	if _, exists := bundle.Skills[req.Name]; exists {
		return nil, fmt.Errorf("skill %q: %w", req.Name, ErrItemExists)
	}

	// Bundle.Path is overloaded; FSDir refuses the values that are not
	// filesystem paths rather than yielding "." and resolving this skill
	// against the process working directory.
	bundleDir, err := bundle.FSDir()
	if err != nil {
		return nil, err
	}
	dir, err := bundles.ResolveSkillDir(bundleDir, req.Name, bundles.BundleSkill{})
	if err != nil {
		return nil, err
	}

	fs := getFS(req.FS)
	if exists, _ := afero.DirExists(fs, dir); exists {
		return nil, fmt.Errorf("skill %q: %w (directory %s already exists)", req.Name, ErrItemExists, dir)
	}

	description := req.Description
	if description == "" {
		description = "TODO: describe when an agent should use this skill."
	}
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create skill directory %s: %w", dir, err)
	}
	skillMDPath := filepath.Join(dir, "SKILL.md")
	// No AllowEmpty: skillTemplate always renders a non-empty document, and
	// dir was just refused-if-existing above, so this is always a fresh path.
	if err := safefs.WriteFile(fs, skillMDPath, []byte(skillTemplate(req.Name, description)), 0o644); err != nil {
		_ = fs.RemoveAll(dir)
		return nil, fmt.Errorf("write %s: %w", skillMDPath, err)
	}

	if _, err := bundles.ParseSkillPackage(fs, dir, 0); err != nil {
		_ = fs.RemoveAll(dir)
		return nil, fmt.Errorf("scaffolded skill %q failed validation: %w", req.Name, err)
	}

	return &CreateSkillResult{Status: "created", Bundle: req.Bundle, Name: req.Name, Dir: dir}, nil
}

// RemoveSkillRequest is the input for RemoveSkill.
type RemoveSkillRequest struct {
	Bundle string `json:"bundle"`
	Name   string `json:"name"`

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
	// FS, when non-nil, is the afero filesystem the skill directory is
	// removed from; nil defaults to the OS filesystem.
	FS afero.Fs `json:"-"`
}

// RemoveSkillResult reports what was removed.
type RemoveSkillResult struct {
	Status string `json:"status"`
	Bundle string `json:"bundle"`
	Name   string `json:"name"`
	Dir    string `json:"dir"`
}

// RemoveSkill deletes a skill package: its directory, which in a tree IS the
// skill — CreateSkill's write surface in reverse. An unknown name is
// ErrItemNotFound, never a silent no-op.
func RemoveSkill(_ context.Context, cfg *config.Config, req RemoveSkillRequest) (*RemoveSkillResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	bundle, err := loadBundleForUpdate(bundleStore(cfg, req.Store), cfg, req.Bundle)
	if err != nil {
		return nil, err
	}
	entry, ok := bundle.Skills[req.Name]
	if !ok {
		return nil, fmt.Errorf("skill %q: %w", req.Name, ErrItemNotFound)
	}

	// Bundle.Path is overloaded; FSDir refuses the values that are not
	// filesystem paths rather than yielding "." and resolving this skill
	// against the process working directory.
	bundleDir, err := bundle.FSDir()
	if err != nil {
		return nil, err
	}
	dir, err := bundles.ResolveSkillDir(bundleDir, req.Name, entry)
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", req.Name, err)
	}

	if err := getFS(req.FS).RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("skill %q: removing %s: %w", req.Name, dir, err)
	}

	return &RemoveSkillResult{Status: "removed", Bundle: req.Bundle, Name: req.Name, Dir: dir}, nil
}

// ExportSkillRequest is the input for ExportSkill.
type ExportSkillRequest struct {
	Bundle  string `json:"bundle"`
	Name    string `json:"name"`
	OutPath string `json:"out_path,omitempty"` // default: "<name>.zip" in the cwd

	// Force allows overwriting an existing file at the output path. Without it, ExportSkill refuses
	// to clobber a pre-existing file at OutPath: the default
	// "<name>.zip" lands in the process cwd, so a second `ctxloom skill
	// export foo` — or any unrelated file already named `foo.zip` — used to
	// be silently destroyed.
	Force bool `json:"force,omitempty"`

	// FS, when non-nil, is the afero filesystem read/written; nil defaults to
	// the OS filesystem.
	FS afero.Fs `json:"-"`
}

// ExportSkillResult reports where the packed archive landed.
type ExportSkillResult struct {
	Name    string `json:"name"`
	ZipPath string `json:"zip_path"`
	Bytes   int    `json:"bytes"`
}

// ExportSkill packs a bundle's skill source tree into an Anthropic-Skills-API
// shaped `.zip` (bundles.ExportSkillZip) — the archive interchange form
// (skill/command split plan §3.1b). Reads through the plain (ungated)
// bundleLoader: exporting your own authored bundle is an authoring action, not
// an exposure surface.
func ExportSkill(_ context.Context, cfg *config.Config, req ExportSkillRequest) (*ExportSkillResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	bundle, err := bundleLoader(cfg).Load(req.Bundle)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Bundle, err)
	}
	entry, ok := bundle.Skills[req.Name]
	if !ok {
		return nil, fmt.Errorf("skill %q: %w", req.Name, ErrItemNotFound)
	}

	fs := getFS(req.FS)
	// Bundle.Path is overloaded; FSDir refuses the values that are not
	// filesystem paths rather than yielding "." and resolving this skill
	// against the process working directory.
	bundleDir, err := bundle.FSDir()
	if err != nil {
		return nil, err
	}
	dir, err := bundles.ResolveSkillDir(bundleDir, req.Name, entry)
	if err != nil {
		return nil, err
	}
	pkg, err := bundles.ParseSkillPackage(fs, dir, 0)
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", req.Name, err)
	}
	zipBytes, err := bundles.ExportSkillZip(fs, dir, pkg)
	if err != nil {
		return nil, fmt.Errorf("export %q: %w", req.Name, err)
	}

	outPath := req.OutPath
	if outPath == "" {
		outPath = req.Name + ".zip"
	}
	// Refuse to silently clobber a pre-existing file at outPath
	// (the default "<name>.zip" lands in the process cwd, so a second export
	// — or any unrelated file already using that name — used to be destroyed
	// with no warning). --force opts into overwriting.
	if !req.Force {
		if exists, eerr := afero.Exists(fs, outPath); eerr == nil && exists {
			return nil, fmt.Errorf("export %q: %s already exists (pass Force/--force to overwrite)", req.Name, outPath)
		}
	}
	// No AllowEmpty: a zip archive's own format bytes are never zero-length.
	if err := safefs.WriteFile(fs, outPath, zipBytes, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", outPath, err)
	}

	return &ExportSkillResult{Name: req.Name, ZipPath: outPath, Bytes: len(zipBytes)}, nil
}

// ImportSkillRequest is the input for ImportSkill.
type ImportSkillRequest struct {
	Bundle      string `json:"bundle"`       // target bundle to land the package in (must already exist)
	ArchivePath string `json:"archive_path"` // .zip or .tar.gz to read

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
	// FS, when non-nil, is the afero filesystem read/written; nil defaults to
	// the OS filesystem.
	FS afero.Fs `json:"-"`
}

// ImportSkillResult reports the landed tree.
type ImportSkillResult struct {
	Status string `json:"status"`
	Bundle string `json:"bundle"`
	Name   string `json:"name"`
	Dir    string `json:"dir"`

	FileCount int `json:"file_count"`
}

// ImportSkill imports a `.zip`/`.tar.gz` Agent Skill archive into a bundle via
// the hardened extractor (bundles.ImportSkillArchive — zip-slip/symlink/
// entry-count/decompression-bomb rejections all apply, unconditionally,
// before this function ever sees a byte) and lands a reviewable package at
// skills/<name>/ — which, in a tree, is the whole of registering it.
func ImportSkill(ctx context.Context, cfg *config.Config, req ImportSkillRequest) (*ImportSkillResult, error) {
	if req.ArchivePath == "" {
		return nil, fmt.Errorf("archive path is required")
	}
	bundle, err := loadBundleForUpdate(bundleStore(cfg, req.Store), cfg, req.Bundle)
	if err != nil {
		return nil, err
	}

	fs := getFS(req.FS)
	archiveBytes, err := afero.ReadFile(fs, req.ArchivePath)
	if err != nil {
		return nil, fmt.Errorf("read archive %s: %w", req.ArchivePath, err)
	}

	// Bundle.Path is overloaded; FSDir refuses the values that are not
	// filesystem paths rather than yielding "." and resolving this skill
	// against the process working directory.
	bundleDir, err := bundle.FSDir()
	if err != nil {
		return nil, err
	}
	skillsParent := filepath.Join(bundleDir, "skills")
	// Validation runs against the STAGING tree, so a malformed archive can
	// never destroy the skill it was supposed to replace: the
	// destination is computed from the ARCHIVE's own top-level directory
	// name, so "import this over the skill I already have" was the ordinary
	// case, not an exotic one.
	var pkg *bundles.SkillPackage
	landedDir, err := bundles.ImportSkillArchive(ctx, fs, archiveBytes, skillsParent, bundles.ExtractOptions{},
		func(vfs afero.Fs, staged string) error {
			p, perr := bundles.ParseSkillPackage(vfs, staged, 0)
			if perr != nil {
				return fmt.Errorf("imported skill failed validation: %w", perr)
			}
			pkg = p
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("import %s: %w", req.ArchivePath, err)
	}

	return &ImportSkillResult{
		Status:    "imported",
		Bundle:    req.Bundle,
		Name:      pkg.Name,
		Dir:       landedDir,
		FileCount: len(pkg.Manifest),
	}, nil
}
