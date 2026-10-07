package content

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// Profile is a bundle-shipped profile.
//
// A profile is not a deliverable item kind, which is why KindProfile is
// defined in this package rather than promoted to an ident.ItemKind constant.
type Profile struct {
	// Name is the profile's identity, taken from its filename.
	Name string
	// Def is profiles.Profile, reused verbatim rather than mirrored. Its
	// FragmentRef already round-trips priority ordering losslessly (a bare
	// string at priority 0, a {name, priority} mapping otherwise), and
	// re-deriving that here would be a second implementation to drift.
	Def profiles.Profile
}

func (Profile) Kind() ident.ItemKind { return KindProfile }

type profileType struct{}

func (profileType) Name() string { return KindProfile.Dir() }
func (profileType) Dir() string  { return KindProfile.Dir() }

// Meta: none. A profile document carries its own fields natively.
func (profileType) Meta() MetaStore { return InlineMeta{} }

func (t profileType) Detect(src Source) bool {
	_, ok := detectSingleYAML(t.Dir(), src, 0)
	return ok
}

// Forms reports FormRaw. A profile is an authored document with exactly one
// materialization; FormNone would claim it binds no content at all, which is
// false — its bytes are hashed and covered like every other component's.
func (t profileType) Forms(src Source) ([]ident.ContentForm, error) {
	if _, ok := detectSingleYAML(t.Dir(), src, 0); !ok {
		return nil, fmt.Errorf("%w: not a profile", ErrUnrecognized)
	}
	return []ident.ContentForm{ident.FormRaw}, nil
}

func (t profileType) RefFor(bundle string, src Source) (ident.Ref, error) {
	name, ok := detectSingleYAML(t.Dir(), src, 0)
	if !ok {
		return ident.Ref{}, fmt.Errorf("%w: not a profile", ErrUnrecognized)
	}
	return ident.Ref{Bundle: bundle, Kind: KindProfile, Name: name}, nil
}

// Decode reads the profile document through profiles.Decode, the one profile
// decoder: schema validation of the document as written plus the normalizer
// stages, so a profile item from any bundle is held to exactly what a profile
// may say.
func (t profileType) Decode(src Source) (Surface, error) {
	name, ok := detectSingleYAML(t.Dir(), src, 0)
	if !ok {
		return nil, fmt.Errorf("%w: not a %s item", ErrUnrecognized, t.Dir())
	}
	paths, err := src.List()
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		if IsMetaPath(p) {
			return nil, refuseUnexplainedMeta(t, name, p)
		}
	}
	file := nonMetaPaths(paths)[0]
	data, err := src.Open(file)
	if err != nil {
		return nil, err
	}
	def, err := profiles.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("content: %s: %w", file, err)
	}
	// Name/Path/Signer are yaml:"-" derived fields on profiles.Profile; the
	// filename is the authority for Name.
	def.Name = name
	return Profile{Name: name, Def: *def}, nil
}

func (t profileType) Encode(s Surface) ([]Component, error) {
	p, ok := s.(Profile)
	if !ok {
		return nil, fmt.Errorf("%w: %T is not a Profile", ErrSurfaceType, s)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("%w: surface has no name", ErrSurfaceType)
	}
	if strings.ContainsAny(p.Name, `/\`) {
		return nil, fmt.Errorf("%w: profile name %q must be a single path segment", ErrBadPath, p.Name)
	}
	body, err := yamlx.Marshal(p.Def)
	if err != nil {
		return nil, fmt.Errorf("content: encoding profile %q: %w", p.Name, err)
	}
	return []Component{{
		Path:  itemPath(t.Dir(), p.Name, ".yaml"),
		Mode:  ModeRegular,
		Bytes: body,
	}}, nil
}
