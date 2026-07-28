package tasks

import (
	semver "github.com/Masterminds/semver/v3"
	tagma "github.com/benjaminabbitt/tagma/ports/go"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// tagmaTypeNamespace is the reserved namespace a tagma.type:<target>=<name>
// config tag lives in (tagma SPEC.md §9) — the identical namespace tagma's
// own ports/go index.go scans for at query time (its unexported
// typeConfigNamespace). This package never reads that constant directly
// (it's unexported across the module boundary); this is the same literal,
// kept in lockstep by tagschema.FacetNamespace + "." + tagschema.TypeFacet
// (== "tagma.type"), exactly as tagschema's own declaration-namespace
// convention requires (see tagschema.Parse's doc).
const tagmaTypeNamespace = tagschema.FacetNamespace + "." + tagschema.TypeFacet

// registerTypes registers every tagma.TypeComparator this package knows
// about against idx (tagma SPEC.md §9). Called unconditionally by
// filterTasks on every tag-query index it builds: registering a name no
// declaration ever references is harmless (tagma's own RegisterType doc),
// so this never needs to consult schema first.
func registerTypes(idx *tagma.Index) {
	idx.RegisterType(tagschema.SemverTypeName, semverComparator{})
}

// typeConfigTags builds the tagma.type:<target>=<name> config tags (SPEC.md
// §9) schema declares under tagschema.TypeFacet — one tag per declared
// target — ready for Index.AddItem under typeConfigItemID. There is no
// RegisterType-adjacent "declare a target" call in tagma's API; the
// declaration itself IS a config tag living in the index, read back out by
// tagma's own query-time typeConfig scan — the identical pattern
// tagschema's arity/hide facets already follow (see that package's doc).
// schema == nil, or one declaring no TypeFacet targets, returns nil.
func typeConfigTags(schema *tagschema.Schema) []tagma.Tag {
	targets := schema.Targets(tagschema.TypeFacet)
	if len(targets) == 0 {
		return nil
	}
	ns := tagmaTypeNamespace
	out := make([]tagma.Tag, 0, len(targets))
	for _, target := range targets {
		name, ok := schema.Get(tagschema.TypeFacet, target)
		if !ok {
			continue
		}
		out = append(out, tagma.Tag{Namespace: &ns, Key: target, Value: &name})
	}
	return out
}

// semverComparator is the tagma.TypeComparator this package registers under
// tagschema.SemverTypeName (SPEC.md §9): full SemVer 2.0.0 precedence via
// github.com/Masterminds/semver/v3's StrictNewVersion/Compare — already a
// direct module dependency (see internal/remote/version_constraint.go), not
// hand-rolled here. StrictNewVersion (not the lenient NewVersion, which
// coerces a two-component "1.2" or a "v"-prefixed value into a version) is
// the write-seam's own validator too (operations.validateTag), so a value
// this comparator can compare is exactly a value the write seam would have
// accepted.
type semverComparator struct{}

// Compare implements tagma.TypeComparator. Either side failing to parse as a
// strict SemVer 2.0.0 version is NotComparable (0, false) — tagma's own
// contract for an atom that can't be evaluated under a declared type (SPEC.md
// §9): the atom simply doesn't match, never an error, and Compare itself
// never panics. Build metadata (SemVer 2.0.0 §10, a "+..." suffix) is
// excluded from precedence by the library's own Compare — verified, not
// assumed, by this package's tests.
func (semverComparator) Compare(a, b string) (int, bool) {
	va, err := semver.StrictNewVersion(a)
	if err != nil {
		return 0, false
	}
	vb, err := semver.StrictNewVersion(b)
	if err != nil {
		return 0, false
	}
	return va.Compare(vb), true
}
