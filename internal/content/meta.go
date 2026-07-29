package content

// MetaStore is how a surface type DECLARES where it keeps its ctxloom metadata.
// It is returned by SurfaceType.Meta, so residency is the type's own decision
// rather than a package-wide rule with per-kind exceptions.
//
// # Why this is a per-type surface and the RECOGNITION of a sidecar is not
//
// Two different questions hide behind "is this file metadata":
//
//   - Is this path SHAPED like metadata? That is package-wide and deliberately
//     so: `.<name>.meta.yaml` is a RESERVED namespace (see IsMetaPath), and the
//     tree walker groups any such file into its item regardless of type. It has
//     to — a sidecar the walker failed to group would leave the digest, and
//     declared executability would stop being attested with everything still
//     green. Making recognition per-type would put that trap back.
//   - Does THIS type legitimately keep metadata there? That is per-type, and it
//     is what this interface answers. A type that keeps metadata in front-matter,
//     or has none at all, returns false from Accepts — which turns a stray
//     metadata-shaped file into a LOUD ERROR at decode rather than bytes that are
//     hashed into the signature and explained by nothing.
//
// So the walker stays kind-agnostic, a stray sidecar can never vanish silently,
// and a type still owns its own storage.
type MetaStore interface {
	// PathFor returns the bundle-relative metadata path for an item in dir, and
	// ok=false when this type keeps no separate metadata file.
	PathFor(dir, name string) (path string, ok bool)
	// Accepts reports whether a metadata-shaped path is legitimate metadata for
	// this type.
	Accepts(dir, path string) bool
}

// SidecarMeta keeps metadata in a dot-prefixed `.<name>.meta.yaml` file beside the
// content.
//
// This is the right choice whenever the content file is consumed by ANOTHER tool
// with its own schema — an MCP server definition, an Agent Skill package — because
// it keeps that file pure: none of our keys in it, so it stays directly usable by
// the tool that owns its schema, and no foreign consumer can reject it for an
// unknown key.
type SidecarMeta struct{}

func (SidecarMeta) PathFor(dir, name string) (string, bool) {
	return MetaPathForName(dir, name), true
}

func (SidecarMeta) Accepts(_, path string) bool { return IsMetaPath(path) }

// InlineMeta keeps metadata inside the content file itself — YAML front-matter for
// the .md kinds — or declares that the type has no ctxloom metadata of its own.
//
// Front-matter is correct for .md because Agent Skills' SKILL.md is front-matter
// based and conforming to that external standard beats diverging from it. It is
// wrong everywhere else, which is why it is a type's declaration and not a default.
type InlineMeta struct{}

func (InlineMeta) PathFor(string, string) (string, bool) { return "", false }

func (InlineMeta) Accepts(string, string) bool { return false }

// Compile-time proof that the shipped layouts satisfy the interface.
var (
	_ MetaStore = SidecarMeta{}
	_ MetaStore = InlineMeta{}
)

// refuseUnexplainedMeta returns an error for a metadata-shaped path the type does
// not accept, naming the residency the type actually declares. It is the single
// place that message is produced, so every kind fails the same way.
func refuseUnexplainedMeta(t SurfaceType, name, path string) error {
	if _, ok := t.Meta().PathFor(t.Dir(), name); ok {
		return nil
	}
	return &unexplainedMetaError{kind: t.Name(), item: name, path: path}
}

type unexplainedMetaError struct {
	kind string
	item string
	path string
}

func (e *unexplainedMetaError) Error() string {
	return "content: " + e.kind + " item " + e.item + " carries the metadata file " + e.path +
		", but " + e.kind + " items keep no separate metadata file" +
		" (its bytes are hashed into this item's signature, so it cannot be ignored)"
}
