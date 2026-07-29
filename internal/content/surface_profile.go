package content

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Profile is a bundle-shipped profile.
//
// It does NOT implement TrustGated, and that absence is the whole point: profiles
// are not governed by the trust gate, and expressing that structurally means
// nobody has to remember a nil check. It is also why KindProfile is defined in
// this package rather than promoted to a trust.ItemKind constant.
type Profile struct {
	// Name is the profile's identity, taken from its filename.
	Name string
	// Def is profiles.Profile, reused verbatim rather than mirrored. Its
	// FragmentRef already round-trips priority ordering losslessly (a bare
	// string at priority 0, a {name, priority} mapping otherwise), and
	// re-deriving that here would be a second implementation to drift.
	Def profiles.Profile
}

func (Profile) Kind() trust.ItemKind { return KindProfile }

type profileType struct{}

func (profileType) Name() string { return KindProfile.Dir() }
func (profileType) Dir() string  { return KindProfile.Dir() }

func (t profileType) Detect(src Source) bool {
	_, ok := detectSingleYAML(t.Dir(), src, 0)
	return ok
}

// Forms reports FormRaw. A profile is an authored document with exactly one
// materialization; FormNone would claim it binds no content at all, which is
// false — its bytes are hashed and covered like every other component's.
func (t profileType) Forms(src Source) ([]signing.Form, error) {
	if _, ok := detectSingleYAML(t.Dir(), src, 0); !ok {
		return nil, fmt.Errorf("%w: not a profile", ErrUnrecognized)
	}
	return []signing.Form{signing.FormRaw}, nil
}

func (t profileType) RefFor(bundle string, src Source) (trust.Ref, error) {
	name, ok := detectSingleYAML(t.Dir(), src, 0)
	if !ok {
		return trust.Ref{}, fmt.Errorf("%w: not a profile", ErrUnrecognized)
	}
	return trust.Ref{Bundle: bundle, Kind: KindProfile, Name: name}, nil
}

func (t profileType) Decode(src Source) (Surface, error) {
	var def profiles.Profile
	name, err := readExecItem(t.Dir(), src, 0, &def, &struct{}{})
	if err != nil {
		return nil, err
	}
	// Name/Path/Signer are yaml:"-" derived fields on profiles.Profile; the
	// filename is the authority for Name, exactly as it is for a directory
	// profile.
	def.Name = name
	return Profile{Name: name, Def: def}, nil
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
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(p.Def); err != nil {
		return nil, fmt.Errorf("content: encoding profile %q: %w", p.Name, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("content: encoding profile %q: %w", p.Name, err)
	}
	return []Component{{
		Path:  itemPath(t.Dir(), p.Name, ".yaml"),
		Mode:  ModeRegular,
		Bytes: buf.Bytes(),
	}}, nil
}
