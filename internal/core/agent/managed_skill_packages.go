package agent

import (
	"path"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file holds WriteManagedSkillPackages: the one skill-materialization
// body every engine's skills approach calls, differing only in the directory.

// WriteManagedSkillPackages materializes every ENABLED skill package under
// skillsDir — `<skillsDir>/<skill name>/SKILL.md` plus every sibling file the
// package carries, each at the mode its export DECLARES — and returns the host
// path of every file it placed, for the caller to declare. It removes
// nothing (WriteManagedPackageFiles).
//
// This is the ONE skill-materialization body in the tree: every engine calls
// it, differing only in the two arguments it takes. A per-engine copy is what
// a shared seam is supposed to make impossible: an engine whose skills materialize through its OWN code
// cannot prove the seam works, it can only prove its own copy does.
//
// A file's MODE comes from PackageFile.Mode — the export's declaration, which
// travelled from the package sidecar's `executable:` list
// (content.DeclaredExecutable). It is never read off the filesystem here or anywhere below:
// a mode bit is not portable, the package digest deliberately excludes it, and
// the declaration is the whole of what a publisher said about executability
// (see content.SkillFile.Mode and content.DeclaredExecutable).
func WriteManagedSkillPackages(files safefs.Root, skillsDir string, skills []SkillExport, opts ...ManagedWriteOption) ([]string, error) {
	return WriteManagedPackageFiles(files, skillsDir, skills,
		func(s SkillExport) bool { return s.Enabled },
		func(s SkillExport) string { return s.Name },
		func(s SkillExport) ([]PackageFile, error) {
			out := make([]PackageFile, len(s.Files))
			for i, f := range s.Files {
				out[i] = PackageFile{
					// The export's paths are package-relative ("SKILL.md",
					// "scripts/run.sh"); the package's own directory is joined
					// on HERE, once, so no engine re-derives it.
					RelPath: path.Join(s.Name, f.RelPath),
					Content: f.Content,
					Mode:    f.Mode,
				}
			}
			return out, nil
		},
		opts...)
}
