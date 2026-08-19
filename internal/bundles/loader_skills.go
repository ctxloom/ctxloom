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
	"github.com/ctxloom/ctxloom/internal/trust"
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
	Tags        []string          // combined (bundle + skill) tags

	// TrustRef is the ref the trust gate keys this package by, minted through
	// the canonical bundle-reference grammar (itemRefFor,
	// "ctxloom+<class>:...#skills/<name>") — the same honest typed-source
	// keying LoadedContent uses. A read FACT, never a decision.
	TrustRef string
	// TrustPayload is the package's trust preimage: the encoded EffectiveManifest
	// (authored when the skill has been through `ctxloom skill sync`, derived
	// from the on-disk tree otherwise). A skill is a directory, so its preimage
	// is a manifest rather than one body blob. The reader has already verified
	// the on-disk tree against this same manifest, so what the process stage
	// decides on is exactly what was verified.
	TrustPayload []byte
	// Signer is the owning bundle's VERIFIED publisher identity, or "" — see
	// LoadedContent.Signer.
	Signer string

	// Read is the owning bundle's read — the trust FACTS its reader established.
	// Authorizer.Admit decides on it (bundles.Exposure); see ItemRead.Read for why
	// exporting a value whose axes are unexported is safe.
	Read BundleRead
}

// LoadedSkillFile is one file of a resolved skill package: its path relative
// to the package's OWN directory (never bundle- or engine-prefixed), its
// bytes, and its POSIX file mode.
type LoadedSkillFile struct {
	RelPath string
	Content []byte
	Mode    uint32 // POSIX permission bits (e.g. 0644, 0755)
}

// ReadBundleSkills reports every skill shipped by the bundle at bundleRef as
// fully-resolved LoadedSkill, in deterministic (name-sorted) order, or nil if
// the bundle can't be loaded. Mirrors ReadBundleCommands: it lets skill exports
// be scoped to a specific profile's bundles rather than a global sweep. A skill
// whose source tree fails to resolve, verify, parse or read is skipped with a
// warning — the reader does not HAVE it — never aborting the rest of the
// bundle's skills. Nothing is dropped on policy grounds; see
// Pipeline.SkillsFromBundleRef.
func (l *Loader) ReadBundleSkills(bundleRef string) []*LoadedSkill {
	if isItemScopedRef(bundleRef) {
		// See ReadBundleCommands: an ITEM-scoped ref ("<bundle>#fragments/<name>")
		// cherry-picks one fragment, not the bundle, and legitimately ships no
		// skills — l.lookup cannot resolve the raw selector-bearing string by
		// design, so warning here was a false positive on every default
		// assembly that used a fragment cherry-pick.
		return nil
	}
	read, ok := l.lookup(bundleRef)
	if !ok {
		// This feeds the export path that WRITES per-engine skill files, so a
		// bare `return nil` exported zero skills, exit 0, in silence —
		// indistinguishable from a bundle that ships none. The
		// sibling expandBundleRef already warns here through this same warner;
		// that inconsistency was the tell.
		l.warnUnresolvedBundle(bundleRef, l.missing(bundleRef))
		return nil
	}
	bundle := read.Bundle
	names := make([]string, 0, len(bundle.Skills))
	for name := range bundle.Skills {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*LoadedSkill, 0, len(names))
	for _, name := range names {
		if ls := l.skillContent(read, name, bundle.Skills[name]); ls != nil {
			out = append(out, ls)
		}
	}
	return out
}

// skillContent resolves one bundle skill entry into a LoadedSkill: it derives
// the package's trust preimage (BundleSkill.EffectiveManifest — the authored
// manifest when the skill has been through `ctxloom skill sync`, otherwise one
// derived from the on-disk tree), VERIFIES the on-disk tree against that same
// manifest, then parses the tree fresh (ParseSkillPackage — the same parse
// authoring/sync uses) and reads every file's bytes.
//
// Verification stays HERE, in the reader: establishing that these bytes are
// what the manifest says is reading, and a mismatch (tampered script, drifted
// content) means the reader does not have a coherent package to report at all.
// It reports nothing, loudly, rather than a partial/tampered one. Deciding
// whether the package may be DELIVERED is the process stage's call, over the
// preimage carried out on TrustPayload.
func (l *Loader) skillContent(read BundleRead, name string, entry BundleSkill) *LoadedSkill {
	bundle := read.Bundle
	// NOT filepath.Dir(bundle.Path): Path is overloaded, and for a companion-
	// or seeded bundle Dir() of it is ".", which resolved this skill against
	// the process working directory and materialized whatever sat there as
	// the bundle's own content. FSDir refuses those values.
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
	// a constant hash AND no tamper check — two failures of one trust gate.
	// A failure to resolve the manifest withholds.
	manifest, err := entry.EffectiveManifest(l.FS(), bundleDir, name)
	if err != nil {
		clidiag.Warn("ctxloom", "skill %q withheld: %v", name, err)
		return nil
	}
	payload, err := skillPayloadFor(manifest)
	if err != nil {
		clidiag.Warn("ctxloom", "skill %q withheld: encoding trust preimage: %v", name, err)
		return nil
	}

	// Runs on EVERY skill, not just synced ones. For an authored manifest
	// this is the tamper check against what was signed; for a derived one it
	// re-confirms the tree still matches the preimage carried out below, so the
	// bytes the process stage decides on are the bytes just verified.
	if verr := VerifyExtractedManifest(l.FS(), dir, manifest); verr != nil {
		clidiag.Warn("ctxloom", "skill %q withheld: %v", name, verr)
		return nil
	}

	pkg, err := ParseSkillPackage(l.FS(), dir, 0)
	if err != nil {
		clidiag.Warn("ctxloom", "skipping skill %q: %v", name, err)
		return nil
	}

	files := make([]LoadedSkillFile, 0, len(pkg.Manifest))
	for _, m := range pkg.Manifest {
		data, rerr := afero.ReadFile(l.FS(), filepath.Join(dir, filepath.FromSlash(m.Path)))
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
		Name:         bundle.Name + "/" + name,
		Bundle:       bundle.Name,
		Item:         name,
		Frontmatter:  pkg.Frontmatter,
		Body:         pkg.Body,
		Files:        files,
		LLM:          entry.LLM,
		Tags:         slices.Concat(bundle.Tags, entry.Tags),
		TrustRef:     itemRefFor(read.SourceRef(), trust.KindSkill, name),
		TrustPayload: payload,
		Signer:       bundle.Signer(),
		Read:         read,
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

// ReadAllSkills reports every Agent Skill package this reader can resolve
// across every bundle, in deterministic (bundle, then name) order. A package
// whose source tree fails to resolve, verify or parse is omitted (skillContent
// already warns) — the reader does not have it. Nothing is dropped on policy
// grounds; see Pipeline.ListAllSkills for the gated listing.
func (l *Loader) ReadAllSkills() ([]*LoadedSkill, error) {
	var out []*LoadedSkill
	for _, read := range l.Reads() {
		bundle := read.Bundle
		for _, name := range bundle.SkillNames() {
			if ls := l.skillContent(read, name, bundle.Skills[name]); ls != nil {
				out = append(out, ls)
			}
		}
	}
	return out, nil
}

// skillInfoFor projects a resolved package into its listing shape. Skills carry
// no single-blob "content" (a skill is a directory tree), so listing surfaces
// file count instead.
func skillInfoFor(ls *LoadedSkill) SkillInfo {
	return SkillInfo{
		Name:        ls.Name,
		Bundle:      ls.Bundle,
		Description: ls.Frontmatter.Description,
		Tags:        ls.Tags,
		FileCount:   len(ls.Files),
	}
}

// ListAllSkills lists every Agent Skill package across every bundle — the skill
// analog of ListAllCommands, and like it a MANAGEMENT/listing read: it reports
// every package, so a pending-review one still shows up to be reviewed.
func (l *Loader) ListAllSkills() ([]SkillInfo, error) {
	skills, err := l.ReadAllSkills()
	if err != nil {
		return nil, err
	}
	out := make([]SkillInfo, 0, len(skills))
	for _, ls := range skills {
		out = append(out, skillInfoFor(ls))
	}
	return out, nil
}

// ReadSkill reports every Agent Skill package this reader holds under name —
// the skill analog of ReadCommand. Name can be "skill-name" (searches all
// bundles, so several bundles may each answer) or "bundle#skills/name".
func (l *Loader) ReadSkill(name string) ([]*LoadedSkill, error) {
	bundleName, skillName, isRef, err := splitItemRef(name, "skills")
	if err != nil {
		return nil, err
	}
	if isRef {
		return l.skillFromBundle(bundleName, skillName)
	}
	return l.searchSkill(name)
}

// skillFromBundle loads a specific bundle and reports the named skill — the
// single-candidate case of ReadSkill. A package the reader could not resolve
// (skillContent already warned) reports as not-found rather than as an empty
// success: a skill that does not materialize must never look like one that
// materialized to nothing.
func (l *Loader) skillFromBundle(bundleName, skillName string) ([]*LoadedSkill, error) {
	read, ok := l.lookup(bundleName)
	if !ok {
		return nil, l.missing(bundleName)
	}
	entry, ok := read.Bundle.Skills[skillName]
	if !ok {
		return nil, fmt.Errorf("%w: %q in bundle %q", errs.ErrSkillNotFound, skillName, bundleName)
	}
	ls := l.skillContent(read, skillName, entry)
	if ls == nil {
		return nil, fmt.Errorf("%w: %s", errs.ErrSkillNotFound, skillName)
	}
	return []*LoadedSkill{ls}, nil
}

// searchSkill scans every bundle for a skill with the given name and reports
// EVERY package it could resolve, in List order — searchFragment's skill twin.
// Reporting all of them is what lets the process stage keep scanning past one
// it withholds: a trusted copy in another bundle still wins.
func (l *Loader) searchSkill(name string) ([]*LoadedSkill, error) {
	var out []*LoadedSkill
	for _, read := range l.Reads() {
		entry, ok := read.Bundle.Skills[name]
		if !ok {
			continue
		}
		if ls := l.skillContent(read, name, entry); ls != nil {
			out = append(out, ls)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", errs.ErrSkillNotFound, name)
	}
	return out, nil
}
