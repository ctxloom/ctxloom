package bundles

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// This file is the LOADER-side resolution of a bundle's Agent Skill packages
// into their runtime form: SKILL.md's frontmatter/body plus every sibling
// file's bytes and mode, ready for a per-engine materializer
// (agent.WriteManagedPackageFiles via a SkillExport). It is the skill analog
// of CommandsFromBundleRef/commandContent (loader_content.go). Part B1 built
// the data model + manifest (skill.go); this is Part B3-seam's loader-side
// wiring: the "how a profile enables items" half the skill/command split plan
// calls out in §3.2 (the UNCURATED default — every profile-referenced
// bundle's skills export by default, per-engine opt-out). The profile
// `skills:` CURATED list (opt-in, mirroring `commands:`) is Part B6 — not
// implemented here.

// LoadedSkill is the resolved runtime form of a bundle's Agent Skill package:
// SKILL.md's validated frontmatter/body plus every file in the package
// (SKILL.md included) with its bytes and mode, plus the per-engine
// enablement the bundle authored. It is the skill counterpart of
// LoadedContent.
type LoadedSkill struct {
	Name        string            // Full identity ("<bundle>/<item>")
	Bundle      string            // Owning bundle's loader name
	Item        string            // Bare skill name within the bundle (the bundle.yaml map key)
	Frontmatter SkillFrontmatter  // SKILL.md's parsed, validated frontmatter
	Body        string            // SKILL.md content after the frontmatter block
	Files       []LoadedSkillFile // every file in the package, SKILL.md included
	LLM         SkillLLMExports   // per-engine enablement
}

// LoadedSkillFile is one file of a resolved skill package: its path relative
// to the package's OWN directory (never bundle- or engine-prefixed), its
// bytes, and its POSIX file mode.
type LoadedSkillFile struct {
	RelPath string
	Content []byte
	Mode    uint32 // POSIX permission bits (e.g. 0644, 0755)
}

// SkillsFromBundleRef returns every skill shipped by the bundle at bundleRef
// as fully-resolved LoadedSkill, in deterministic (name-sorted) order, or nil
// if the bundle can't be loaded. Mirrors CommandsFromBundleRef: it lets skill
// exports be scoped to a specific profile's bundles rather than a global
// sweep. A skill the trust gate withholds, or whose source tree fails to
// parse/read, is skipped with a warning (skillContent returns nil) — never
// aborting the rest of the bundle's skills.
func (l *Loader) SkillsFromBundleRef(bundleRef string) []*LoadedSkill {
	bundle, err := l.Load(bundleRef)
	if err != nil {
		// This feeds the export path that WRITES per-engine skill files, so a
		// bare `return nil` exported zero skills, exit 0, in silence —
		// indistinguishable from a bundle that ships none (U030-F07). The
		// sibling expandBundleRef already warns here through this same warner;
		// that inconsistency was the tell.
		l.warnUnresolvedBundle(bundleRef, err)
		return nil
	}
	names := make([]string, 0, len(bundle.Skills))
	for name := range bundle.Skills {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*LoadedSkill, 0, len(names))
	for _, name := range names {
		if ls := l.skillContent(bundle, name, bundle.Skills[name]); ls != nil {
			out = append(out, ls)
		}
	}
	return out
}

// skillContent resolves one bundle skill entry into a LoadedSkill: it gates
// the skill through the SAME per-item trust choke as fragments/commands
// (keyed "<bundle>#skills/<name>", trust.KindSkill's selector directory),
// then parses its source tree fresh (ParseSkillPackage — the same parse
// authoring/sync uses) and reads every file's bytes.
//
// The gate decides on a preimage that ALWAYS covers real content
// (BundleSkill.EffectiveManifest): the authored manifest when the skill has
// been through `ctxloom skill sync`, otherwise one derived from the on-disk
// tree. The on-disk tree is then verified against that same manifest in
// either case — a mismatch (tampered script, drifted content) withholds the
// skill loudly rather than materializing a partial/tampered package.
func (l *Loader) skillContent(bundle *Bundle, name string, entry BundleSkill) *LoadedSkill {
	// NOT filepath.Dir(bundle.Path): Path is overloaded, and for a companion-
	// or seeded bundle Dir() of it is ".", which resolved this skill against
	// the process working directory and materialized whatever sat there as
	// the bundle's own content (U030-F03). FSDir refuses those values.
	bundleDir, err := bundle.FSDir()
	if err != nil {
		clidiag.Warn("ctxloom", "skill %q withheld: %v", name, err)
		return nil
	}
	dir, err := ResolveSkillDir(bundleDir, name, entry)
	if err != nil {
		clidiag.Warn("ctxloom", "skipping skill %q: %v", name, err)
		return nil
	}

	// ONE manifest drives both the trust preimage and the integrity check.
	// Authored when the skill has been synced; derived from the on-disk tree
	// when it has not. There is deliberately NO `len(entry.Files) > 0`
	// predicate here any more: that single condition used to both empty the
	// preimage and skip the verification below, so a manifest-less skill got
	// a constant hash AND no tamper check — two failures of one trust gate
	// (arch-review S2/T4). A failure to resolve the manifest withholds.
	manifest, err := entry.EffectiveManifest(l.fs, bundleDir, name)
	if err != nil {
		clidiag.Warn("ctxloom", "skill %q withheld: %v", name, err)
		return nil
	}
	payload, err := skillPayloadFor(manifest)
	if err != nil {
		clidiag.Warn("ctxloom", "skill %q withheld: encoding trust preimage: %v", name, err)
		return nil
	}
	if !l.gateContent(bundle.contentSourceRef(), "skills", name, payload, FormRaw, bundle.Signer()) {
		return nil
	}

	// Runs on EVERY skill, not just synced ones. For an authored manifest
	// this is the tamper check against what was signed; for a derived one it
	// re-reads the tree and re-confirms it still matches the bytes the gate
	// just decided on, closing the window between the decision and the read
	// loop below.
	if verr := VerifyExtractedManifest(l.fs, dir, manifest); verr != nil {
		clidiag.Warn("ctxloom", "skill %q withheld: %v", name, verr)
		return nil
	}

	pkg, err := ParseSkillPackage(l.fs, dir, 0)
	if err != nil {
		clidiag.Warn("ctxloom", "skipping skill %q: %v", name, err)
		return nil
	}

	files := make([]LoadedSkillFile, 0, len(pkg.Manifest))
	for _, m := range pkg.Manifest {
		data, rerr := afero.ReadFile(l.fs, filepath.Join(dir, filepath.FromSlash(m.Path)))
		if rerr != nil {
			clidiag.Warn("ctxloom", "skipping skill %q: reading %s: %v", name, m.Path, rerr)
			return nil
		}
		mode, perr := strconv.ParseUint(m.Mode, 8, 32)
		if perr != nil {
			mode = 0644
		}
		files = append(files, LoadedSkillFile{RelPath: m.Path, Content: data, Mode: uint32(mode)})
	}

	return &LoadedSkill{
		Name:        bundle.Name + "/" + name,
		Bundle:      bundle.Name,
		Item:        name,
		Frontmatter: pkg.Frontmatter,
		Body:        pkg.Body,
		Files:       files,
		LLM:         entry.LLM,
	}
}

// SkillInfo provides metadata about an Agent Skill package for listing —
// the skill analog of ContentInfo. Skills carry no single-blob "content" (a
// skill is a directory tree), so listing surfaces file count instead.
type SkillInfo struct {
	Name        string   // Full name (bundle/item)
	Bundle      string   // Bundle name this came from
	Description string   // SKILL.md frontmatter description
	Tags        []string // Combined (bundle + skill) tags
	FileCount   int      // Number of files in the package (SKILL.md included)
}

// ListAllSkills returns info about every Agent Skill package across every
// bundle, in deterministic (bundle, then name) order — the skill analog of
// ListAllCommands. A skill the trust gate withholds, or whose source tree
// fails to parse, is omitted (skillContent already warns).
func (l *Loader) ListAllSkills() ([]SkillInfo, error) {
	bundleInfos, err := l.List()
	if err != nil {
		return nil, err
	}

	var out []SkillInfo
	for _, bi := range bundleInfos {
		bundle, err := l.LoadFile(bi.Path)
		if err != nil {
			continue
		}
		for _, name := range bundle.SkillNames() {
			ls := l.skillContent(bundle, name, bundle.Skills[name])
			if ls == nil {
				continue
			}
			out = append(out, SkillInfo{
				Name:        ls.Name,
				Bundle:      ls.Bundle,
				Description: ls.Frontmatter.Description,
				Tags:        slices.Concat(bundle.Tags, bundle.Skills[name].Tags),
				FileCount:   len(ls.Files),
			})
		}
	}
	return out, nil
}

// GetSkill finds and loads an Agent Skill package by name — the skill analog
// of GetCommand. Name can be "skill-name" (searches all bundles) or
// "bundle#skills/name".
func (l *Loader) GetSkill(name string) (*LoadedSkill, error) {
	bundleName, skillName, isRef, err := splitItemRef(name, "skills")
	if err != nil {
		return nil, err
	}
	if isRef {
		return l.skillFromBundle(bundleName, skillName)
	}
	return l.searchSkill(name)
}

// skillFromBundle loads a specific bundle and returns the named skill.
func (l *Loader) skillFromBundle(bundleName, skillName string) (*LoadedSkill, error) {
	bundle, err := l.Load(bundleName)
	if err != nil {
		return nil, err
	}
	entry, ok := bundle.Skills[skillName]
	if !ok {
		return nil, fmt.Errorf("%w: %q in bundle %q", errs.ErrSkillNotFound, skillName, bundleName)
	}
	ls := l.skillContent(bundle, skillName, entry)
	if ls == nil {
		return nil, fmt.Errorf("%w: %s", errs.ErrSkillWithheld, skillName)
	}
	return ls, nil
}

// searchSkill scans every bundle for a skill with the given name. A
// gate-withheld match does not end the scan — a trusted copy in another
// bundle still wins; only when every match is withheld does it report
// ErrSkillWithheld (distinct from not-found).
func (l *Loader) searchSkill(name string) (*LoadedSkill, error) {
	bundleInfos, err := l.List()
	if err != nil {
		return nil, err
	}
	withheld := false
	for _, bi := range bundleInfos {
		bundle, err := l.LoadFile(bi.Path)
		if err != nil {
			continue
		}
		entry, ok := bundle.Skills[name]
		if !ok {
			continue
		}
		if ls := l.skillContent(bundle, name, entry); ls != nil {
			return ls, nil
		}
		withheld = true
	}
	if withheld {
		return nil, fmt.Errorf("%w: %s", errs.ErrSkillWithheld, name)
	}
	return nil, fmt.Errorf("%w: %s", errs.ErrSkillNotFound, name)
}
