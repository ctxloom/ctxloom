package content

import (
	"path"
	"strings"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// MetaSuffix is the suffix of a metadata sidecar. Combined with the leading dot
// that MetaPath adds, a sidecar for "mcp/postgres.yaml" is
// "mcp/.postgres.meta.yaml".
//
// Sidecars are DOT-PREFIXED so a loose `ls` or an editor file tree shows content
// files rather than metadata noise, matching this repo's convention of
// dot-prefixing machinery. That choice carries a trap worth stating at the
// definition: dotfiles are excluded by default in much glob and walk code —
// filepath.Glob("*") does not match them. A walker that misses a sidecar drops
// it from the digest, and declared executability then silently stops being
// attested while every signature still verifies and everything looks green.
// The tree walker in this package therefore reads directories directly and
// never globs, and there is a test asserting a dot-prefixed sidecar appears in
// Components AND changes Content.
const MetaSuffix = ".meta.yaml"

// MetaPath returns the metadata sidecar path for a content component path.
// "fragments/solid.md" -> "fragments/.solid.meta.yaml".
func MetaPath(contentPath string) string {
	dir, base := path.Split(contentPath)
	base = strings.TrimSuffix(base, path.Ext(base))
	return dir + "." + base + MetaSuffix
}

// MetaPathForName returns the metadata sidecar path for an item named name in a
// kind directory, for the case where the item has no single content file of its
// own to derive it from — a skill package, whose sidecar sits BESIDE the package
// directory so the package itself stays a pure Agent Skill tree.
//
// The dot goes on the LAST segment, not the first. An item name may contain a
// separator ("pre_tool/guard" for a hook), and dotting the whole name would
// produce "hooks/.pre_tool/guard.meta.yaml" — a HIDDEN DIRECTORY holding a file
// that IsMetaPath does not recognise, so the walker would neither group it into
// the item nor refuse it. That is the shape in which bytes ride along a tree
// unattested, which is exactly what sidecar recognition exists to prevent.
func MetaPathForName(dir, name string) string {
	parent, base := path.Split(name)
	return dir + "/" + parent + "." + base + MetaSuffix
}

// IsMetaPath reports whether a path is a metadata sidecar.
func IsMetaPath(p string) bool {
	base := path.Base(p)
	return strings.HasPrefix(base, ".") && strings.HasSuffix(base, MetaSuffix) && base != "."+MetaSuffix
}

// logicalBase strips a path down to the name that grouping and form detection
// work on: the basename with any sidecar decoration and any extension removed.
//
//	fragments/solid.md              -> solid
//	fragments/solid.distilled.md    -> solid.distilled
//	fragments/.solid.meta.yaml      -> solid
//	mcp/.postgres.meta.yaml         -> postgres
func logicalBase(p string) string {
	base := path.Base(p)
	if IsMetaPath(p) {
		return strings.TrimSuffix(strings.TrimPrefix(base, "."), MetaSuffix)
	}
	return strings.TrimSuffix(base, path.Ext(base))
}

// stemOf reduces a path to the item stem it groups under: logicalBase with any
// trailing form suffix removed. "solid.distilled.md" and "solid.md" and
// ".solid.meta.yaml" all stem to "solid", which is what makes a content file
// and its sidecar and its distilled sibling one candidate rather than three
// items.
func stemOf(p string) string {
	lb := logicalBase(p)
	for _, f := range formSuffixForms {
		if trimmed, ok := strings.CutSuffix(lb, "."+string(f)); ok {
			return trimmed
		}
	}
	return lb
}

// formSuffixForms are the forms that appear as a filename suffix. FormRaw
// deliberately does NOT: it is the base form, carried by an unsuffixed
// filename, so the common single-form item stays a single plainly-named file.
//
// This is the axis that reaches FILENAMES, which is why it is the plain
// raw/distilled vocabulary and not the composite one a countersignature binds:
// the suffix is ".distilled.md" and could never be ".fragment/distilled.md".
var formSuffixForms = []signing.Form{signing.FormDistilled}

// formOf reports which form a component belongs to, given the forms the item
// carries.
//
// This is the layout convention that lets Store/Bundle/Item/Form partition
// components by form with no per-kind branching: a component whose logical base
// ends in ".<form>" belongs to that form, and an unsuffixed component belongs to
// forms[0], the BASE form the type reported first. So fragments/solid.md is raw,
// fragments/solid.distilled.md is distilled, and mcp/postgres.yaml — unsuffixed,
// single-form — is raw too: one rule, and a newly registered kind gets it for
// free.
func formOf(p string, forms []signing.Form) signing.Form {
	lb := logicalBase(p)
	for _, f := range forms {
		if f == "" {
			continue
		}
		if strings.HasSuffix(lb, "."+string(f)) {
			return f
		}
	}
	if len(forms) == 0 {
		return signing.FormNone
	}
	return forms[0]
}

// relToKind strips a type's directory prefix from a bundle-relative path,
// yielding the path within the kind directory. It reports ok=false for a path
// that is not under the directory at all, which lets a Detect implementation
// fail closed instead of misreading a stray path.
func relToKind(dir, p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, dir+"/")
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// nonMetaPaths returns the paths in a candidate group that are not metadata
// sidecars — the content files. Every Detect implementation reasons over these.
func nonMetaPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !IsMetaPath(p) {
			out = append(out, p)
		}
	}
	return out
}
