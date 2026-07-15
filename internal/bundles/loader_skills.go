package bundles

import (
	"path/filepath"
	"sort"
	"strconv"

	"github.com/spf13/afero"

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
// authoring/sync uses) and reads every file's bytes. When the bundle.yaml
// entry carries an authored (signed) manifest already, the on-disk tree is
// verified against it first — a mismatch (tampered script, drifted content)
// withholds the skill loudly rather than materializing a partial/tampered
// package. An entry with no authored manifest yet (no `ctxloom skill sync`
// has run — that CLI is Part B6) is trusted to the fresh parse alone; a
// project-local bundle's local-tier trust already auto-allows it regardless.
func (l *Loader) skillContent(bundle *Bundle, name string, entry BundleSkill) *LoadedSkill {
	payload, err := entry.ContentPayload()
	if err != nil {
		return nil
	}
	if !l.gateContent(bundle.contentSourceRef(), "skills", name, payload, FormRaw, bundle.Signer()) {
		return nil
	}

	bundleDir := filepath.Dir(bundle.Path)
	dir, err := ResolveSkillDir(bundleDir, name, entry)
	if err != nil {
		clidiag.Warn("ctxloom", "skipping skill %q: %v", name, err)
		return nil
	}

	if len(entry.Files) > 0 {
		if verr := VerifyExtractedManifest(l.fs, dir, entry.ToManifest()); verr != nil {
			clidiag.Warn("ctxloom", "skill %q withheld: %v", name, verr)
			return nil
		}
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
