package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// This file is the operations core for `ctxloom skill`: author (create),
// curate the per-file manifest (sync), inspect (list/show), and interchange
// (export/import) an Agent Skill package. It plays the same frontend-agnostic
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
// create` writes. name is both the frontmatter `name` (must equal the
// directory name — bundles.validateSkillFrontmatter's hard constraint) and
// the heading; description is a TODO the author must replace before the
// skill is useful (an empty/placeholder description still passes the length
// check, so this does not block create, but IS a signal for `sync`/review to
// flag — left to the human, never silently filled in with something untrue).
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
// valid frontmatter) inside an EXISTING bundle's content tree, and registers
// it in bundle.yaml's `skills:` map. The bundle must already be directory-form
// (bundle.yaml + skills/<name>/) — skills are unsupported in a single-file
// bundle (loud validation error, never a silent skip; mirrors the loader's own
// detectLegacySkillsKey/dir-form gate). The scaffold is validated with
// ParseSkillPackage before anything is registered — a template that wouldn't
// itself pass validation is never left on disk claiming success.
func CreateSkill(_ context.Context, cfg *config.Config, req CreateSkillRequest) (*CreateSkillResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Bundle)
	if err != nil {
		return nil, err
	}
	if filepath.Base(bundle.Path) != "bundle.yaml" {
		return nil, fmt.Errorf("bundle %q: skills require a directory-form bundle (bundle.yaml + skills/<name>/), not a single-file bundle (%s) — re-init as a directory bundle first",
			req.Bundle, filepath.Base(bundle.Path))
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
	entry := bundles.BundleSkill{}
	dir, err := bundles.ResolveSkillDir(bundleDir, req.Name, entry)
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
	if err := iox.WriteFileAtomicFs(fs, skillMDPath, []byte(skillTemplate(req.Name, description)), 0o644); err != nil {
		_ = fs.RemoveAll(dir)
		return nil, fmt.Errorf("write %s: %w", skillMDPath, err)
	}

	if _, err := bundles.ParseSkillPackage(fs, dir, 0); err != nil {
		_ = fs.RemoveAll(dir)
		return nil, fmt.Errorf("scaffolded skill %q failed validation: %w", req.Name, err)
	}

	if bundle.Skills == nil {
		bundle.Skills = make(map[string]bundles.BundleSkill)
	}
	bundle.Skills[req.Name] = entry
	if err := store.Save(bundle); err != nil {
		_ = fs.RemoveAll(dir)
		return nil, fmt.Errorf("failed to save bundle: %w", err)
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

// RemoveSkill deletes a skill package: its bundle.yaml `skills:`
// registration AND its on-disk directory tree — CreateSkill's write surface
// in reverse. A skill package could be created and never removed until this;
// an unknown name is ErrItemNotFound, never a silent no-op.
//
// The registration is dropped and SAVED first, the directory tree removed
// second: if the directory removal then fails (a permissions problem, a
// concurrent process holding a file open), the store already reflects the
// truth — no listing surface can find a directory that is about to vanish —
// and the error names the orphaned path for a human to clear by hand, rather
// than leaving bundle.yaml pointing at a directory whose removal already
// half-succeeded.
func RemoveSkill(_ context.Context, cfg *config.Config, req RemoveSkillRequest) (*RemoveSkillResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Bundle)
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

	delete(bundle.Skills, req.Name)
	if err := store.Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}

	fs := getFS(req.FS)
	if err := fs.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("skill %q: bundle.yaml no longer lists it, but removing %s failed: %w — remove it by hand", req.Name, dir, err)
	}

	return &RemoveSkillResult{Status: "removed", Bundle: req.Bundle, Name: req.Name, Dir: dir}, nil
}

// SyncSkillRequest is the input for SyncSkill.
type SyncSkillRequest struct {
	Bundle string `json:"bundle"`
	Name   string `json:"name"` // empty = sync every skill in the bundle

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
	// FS, when non-nil, is the afero filesystem the skill tree is read from;
	// nil defaults to the OS filesystem.
	FS afero.Fs `json:"-"`
}

// SyncedSkill reports one skill's recomputed manifest.
type SyncedSkill struct {
	Name      string `json:"name"`
	FileCount int    `json:"file_count"`
	// Changed reports whether the recomputed manifest hash differs from what
	// bundle.yaml had recorded before this sync (a fresh `files:` map, or any
	// content/mode edit since the last sync).
	Changed bool `json:"changed"`
}

// SyncSkillResult reports every skill synced.
type SyncSkillResult struct {
	Status string        `json:"status"`
	Bundle string        `json:"bundle"`
	Synced []SyncedSkill `json:"synced"`
}

// SyncSkill recomputes the per-file manifest (sha256 + mode) for a skill's
// source tree via bundles.ParseSkillPackage — the SAME parse the loader uses
// to verify a tree against a previously-signed manifest — and writes it into
// bundle.yaml's `skills.<name>.files` map. This is what activates the
// install-time tamper check (VerifyExtractedManifest /
// PublisherSkillSignatureVerifier): before a sync ever runs, `files:` is
// empty and skillContent trusts a fresh parse unconditionally (a noted
// gap); after sync, any drift between bundle.yaml's recorded manifest and the
// on-disk tree is a loud withhold, not a silent pass-through. req.Name empty
// syncs every skill the bundle ships.
func SyncSkill(_ context.Context, cfg *config.Config, req SyncSkillRequest) (*SyncSkillResult, error) {
	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Bundle)
	if err != nil {
		return nil, err
	}
	targets, err := skillSyncTargets(bundle, req.Name)
	if err != nil {
		return nil, err
	}

	fs := getFS(req.FS)
	// Bundle.Path is overloaded; FSDir refuses the values that are not
	// filesystem paths rather than yielding "." and resolving this skill
	// against the process working directory.
	bundleDir, err := bundle.FSDir()
	if err != nil {
		return nil, err
	}
	synced := make([]SyncedSkill, 0, len(targets))
	for _, name := range targets {
		entry := bundle.Skills[name]
		dir, err := bundles.ResolveSkillDir(bundleDir, name, entry)
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", name, err)
		}
		pkg, err := bundles.ParseSkillPackage(fs, dir, 0)
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", name, err)
		}

		oldHash := entry.ToManifest().Hash()
		newHash := pkg.Manifest.Hash()

		files := make(map[string]bundles.SkillFileMeta, len(pkg.Manifest))
		for _, m := range pkg.Manifest {
			files[m.Path] = bundles.SkillFileMeta{SHA256: m.SHA256, Mode: m.Mode}
		}
		entry.Files = files
		bundle.Skills[name] = entry

		synced = append(synced, SyncedSkill{Name: name, FileCount: len(files), Changed: oldHash != newHash})
	}

	if err := store.Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}
	return &SyncSkillResult{Status: "synced", Bundle: req.Bundle, Synced: synced}, nil
}

// skillSyncTargets resolves which skill names SyncSkill should recompute:
// name (if it exists), or every skill the bundle ships when name is empty. An
// explicit name absent from the bundle is ErrItemNotFound (loud, never a
// silent no-op sync of zero skills).
func skillSyncTargets(bundle *bundles.Bundle, name string) ([]string, error) {
	if name != "" {
		if _, ok := bundle.Skills[name]; !ok {
			return nil, fmt.Errorf("skill %q: %w", name, ErrItemNotFound)
		}
		return []string{name}, nil
	}
	names := bundle.SkillNames()
	if len(names) == 0 {
		return nil, fmt.Errorf("bundle %q has no skills to sync", bundle.Name)
	}
	return names, nil
}

// ExportSkillRequest is the input for ExportSkill.
type ExportSkillRequest struct {
	Bundle  string `json:"bundle"`
	Name    string `json:"name"`
	OutPath string `json:"out_path,omitempty"` // default: "<name>.zip" in the cwd

	// Sign detaches-signs the exported manifest (the SAME bytes
	// PublisherSkillSignatureVerifier verifies on import) alongside the zip,
	// reusing the existing publish signing machinery — no new scheme. Signer
	// must be supplied when Sign is true; ExportSkill never resolves a key
	// itself (key discovery is internal/signing/agentkey's job, mirroring
	// SignBundleFile).
	Sign   bool       `json:"sign,omitempty"`
	Signer ssh.Signer `json:"-"`

	// Force allows overwriting an existing file at the output path (the zip,
	// and its .sig sibling when Sign is set). Without it, ExportSkill refuses
	// to clobber a pre-existing file at OutPath: the default
	// "<name>.zip" lands in the process cwd, so a second `ctxloom skill
	// export foo` — or any unrelated file already named `foo.zip` — used to
	// be silently destroyed.
	Force bool `json:"force,omitempty"`

	// FS, when non-nil, is the afero filesystem read/written; nil defaults to
	// the OS filesystem.
	FS afero.Fs `json:"-"`
}

// ExportSkillResult reports where the packed archive (and optional detached
// signature) landed.
type ExportSkillResult struct {
	Name    string `json:"name"`
	ZipPath string `json:"zip_path"`
	SigPath string `json:"sig_path,omitempty"`
	Bytes   int    `json:"bytes"`
}

// ExportSkill packs a bundle's skill source tree into an Anthropic-Skills-API
// shaped `.zip` (bundles.ExportSkillZip) — the archive interchange form
// (skill/command split plan §3.1b). Reads through the plain (ungated)
// bundleLoader: exporting your own authored bundle is an authoring action, not
// an exposure surface, mirroring `ctxloom sign`'s own read path.
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
	// A --sign request with no signer is a caller-configuration
	// error, checked BEFORE any write — it used to run after the zip landed
	// on disk, so a caller who fixed the missing signer and retried found a
	// stale unsigned zip masquerading as a fresh export.
	if req.Sign && req.Signer == nil {
		return nil, fmt.Errorf("export %q: --sign requires a signer", req.Name)
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
	if err := iox.WriteFileAtomicFs(fs, outPath, zipBytes, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", outPath, err)
	}

	result := &ExportSkillResult{Name: req.Name, ZipPath: outPath, Bytes: len(zipBytes)}
	if req.Sign {
		// Sign the MANIFEST bytes, not the zip bytes: this is exactly what
		// PublisherSkillSignatureVerifier verifies against on import (it
		// recomputes the manifest from the extracted tree and checks the
		// signature covers THAT), so a sig produced here round-trips through
		// import without inventing a second preimage.
		armored, err := signing.Sign(pkg.Manifest.Serialize(), req.Signer, signing.NamespacePublish)
		if err != nil {
			// A --sign failure used to leave the just-written zip
			// on disk, unsigned, looking exactly like an export that never
			// asked to be signed. Clean it up so a failed signed export
			// leaves nothing behind to be mistaken for a successful one.
			_ = fs.Remove(outPath)
			return nil, fmt.Errorf("sign %q: %w", req.Name, err)
		}
		sigPath := outPath + bundles.SigSuffix
		// No AllowEmpty: armored is signing.Sign's output, never empty.
		if err := iox.WriteFileAtomicFs(fs, sigPath, armored, 0o644); err != nil {
			_ = fs.Remove(outPath)
			return nil, fmt.Errorf("write %s: %w", sigPath, err)
		}
		result.SigPath = sigPath
	}
	return result, nil
}

// ImportSkillRequest is the input for ImportSkill.
type ImportSkillRequest struct {
	Bundle      string `json:"bundle"`       // target bundle to land the tree in (must already exist, directory-form)
	ArchivePath string `json:"archive_path"` // .zip or .tar.gz to read

	// SigPath, when set, is a detached signature covering the archive's
	// manifest bytes (as ExportSkill --sign produces). VERIFIED against the
	// trust root via bundles.PublisherSkillSignatureVerifier BEFORE
	// acceptance — this is PublisherSkillSignatureVerifier wired to a
	// live command for the first time. No signature (SigPath empty) is not a
	// failure: the import still lands, reported unsigned — ctxloom does not
	// require signing to accept content, only to auto-trust it, which this
	// never does either way (see SignatureState doc).
	SigPath string `json:"sig_path,omitempty"`

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
	// FS, when non-nil, is the afero filesystem read/written; nil defaults to
	// the OS filesystem.
	FS afero.Fs `json:"-"`
	// Root resolves which keys are trusted to publish (signing.TrustRoot);
	// nil uses cfg.TrustRoot() (embedded + user + project allowed_signers,
	// unioned).
	Root signing.TrustRoot `json:"-"`
}

// ImportSkillResult reports the landed tree and the signature's verification
// outcome.
type ImportSkillResult struct {
	Status string `json:"status"`
	Bundle string `json:"bundle"`
	Name   string `json:"name"`
	Dir    string `json:"dir"`

	FileCount int `json:"file_count"`

	// SignatureState is one of "unsigned" (no SigPath given), "verified"
	// (SigPath given, cryptographically covers the extracted tree's
	// manifest, by a key this machine trusts to publish), or "unverified:
	// <reason>" (a signature was given but does not verify — either
	// tampered, or by a key not trusted here). NONE of these three outcomes
	// changes whether the tree lands: the import always lands a
	// structurally-valid tree (HardenedExtract/ParseSkillPackage already
	// gate that) as pending-review, exactly like any other freshly-pulled
	// remote content — this field is reporting only. A verified signature is
	// NOT auto-trust; the ordinary `ctxloom review`/`trust` flow still
	// governs whether the skill is ever actually exposed (skillContent's
	// trust gate, loader_skills.go).
	SignatureState string `json:"signature_state"`
}

// ImportSkill imports a `.zip`/`.tar.gz` Agent Skill archive into a bundle via
// the hardened extractor (bundles.ImportSkillArchive — zip-slip/symlink/
// entry-count/decompression-bomb rejections all apply, unconditionally,
// before this function ever sees a byte), lands a reviewable tree, and
// registers/updates the bundle.yaml `skills:` entry with the freshly-computed
// manifest (so a subsequent `ctxloom skill sync` is a no-op until the tree
// changes again). If SigPath is given, the signature is verified against the
// tree's own recomputed manifest via bundles.PublisherSkillSignatureVerifier
// — wired to a live command for the first time — but a failed
// or absent verification never blocks the import: "do not auto-trust remote
// content" means neither branch auto-accepts trust, not that an untrusted
// import is destroyed. Only a structurally invalid archive/tree (extractor
// rejection, or a post-extraction SKILL.md/frontmatter that fails
// ParseSkillPackage) is refused and cleaned up.
func ImportSkill(_ context.Context, cfg *config.Config, req ImportSkillRequest) (*ImportSkillResult, error) {
	if req.ArchivePath == "" {
		return nil, fmt.Errorf("archive path is required")
	}
	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Bundle)
	if err != nil {
		return nil, err
	}
	if filepath.Base(bundle.Path) != "bundle.yaml" {
		return nil, fmt.Errorf("bundle %q: skills require a directory-form bundle (bundle.yaml + skills/<name>/), not a single-file bundle (%s) — re-init as a directory bundle first",
			req.Bundle, filepath.Base(bundle.Path))
	}

	fs := getFS(req.FS)
	archiveBytes, err := afero.ReadFile(fs, req.ArchivePath)
	if err != nil {
		return nil, fmt.Errorf("read archive %s: %w", req.ArchivePath, err)
	}

	// Read the signature BEFORE anything is extracted: an unreadable --sig
	// path is a caller error that has nothing to do with the destination, and
	// discovering it after the swap meant deleting a good skill tree over a
	// typo'd path (a second loss path).
	var sigBytes []byte
	if req.SigPath != "" {
		var serr error
		sigBytes, serr = afero.ReadFile(fs, req.SigPath)
		if serr != nil {
			return nil, fmt.Errorf("read signature %s: %w", req.SigPath, serr)
		}
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
	landedDir, err := bundles.ImportSkillArchive(fs, archiveBytes, skillsParent, bundles.ExtractOptions{},
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

	sigState := "unsigned"
	if req.SigPath != "" {
		root := req.Root
		if root == nil {
			root = cfg.TrustRoot()
		}
		verifier := bundles.PublisherSkillSignatureVerifier{ArmoredSignature: sigBytes, Root: root}
		if verr := verifier.VerifyManifestSignature(pkg.Manifest); verr != nil {
			sigState = fmt.Sprintf("unverified: %v", verr)
		} else {
			sigState = "verified"
		}
	}

	name := pkg.Name
	rel, relErr := filepath.Rel(bundleDir, landedDir)
	if relErr != nil {
		rel = filepath.ToSlash(filepath.Join("skills", name))
	}

	if bundle.Skills == nil {
		bundle.Skills = make(map[string]bundles.BundleSkill)
	}
	// Preserve any existing Tags/Notes/LLM enablement on a re-import over the
	// same name; only Path and the recomputed Files change.
	entry := bundle.Skills[name]
	entry.Path = filepath.ToSlash(rel)
	files := make(map[string]bundles.SkillFileMeta, len(pkg.Manifest))
	for _, m := range pkg.Manifest {
		files[m.Path] = bundles.SkillFileMeta{SHA256: m.SHA256, Mode: m.Mode}
	}
	entry.Files = files
	bundle.Skills[name] = entry

	if err := store.Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}

	return &ImportSkillResult{
		Status:         "imported",
		Bundle:         req.Bundle,
		Name:           name,
		Dir:            landedDir,
		FileCount:      len(pkg.Manifest),
		SignatureState: sigState,
	}, nil
}
