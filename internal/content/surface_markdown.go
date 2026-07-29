package content

import (
	"fmt"
	"path"
	"strings"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// mdMeta is the YAML front-matter DTO for the .md content kinds.
//
// It is a SERIALIZATION shape, not a surface: Fragment and Command each declare
// their own fields, and each populates only the subset it owns (a fragment never
// carries a description or per-engine exports). Front-matter is used here, and
// ONLY here, because Agent Skills' SKILL.md is front-matter-based and .md
// authoring conventions follow that external standard; every other kind uses a
// sidecar.
type mdMeta struct {
	Description  string        `yaml:"description,omitempty"`
	Tags         []string      `yaml:"tags,omitempty"`
	Notes        string        `yaml:"notes,omitempty"`
	Installation string        `yaml:"installation,omitempty"`
	ContentHash  string        `yaml:"content_hash,omitempty"`
	NoDistill    bool          `yaml:"no_distill,omitempty"`
	DistilledBy  string        `yaml:"distilled_by,omitempty"`
	Exports      EngineExports `yaml:"exports,omitempty"`
}

// detectMarkdownItem is the shared recognition for the .md content kinds: every
// non-sidecar component is a direct .md child of the kind directory, and they all
// share one stem. It returns the stem.
//
// It fails closed on anything else — a nested path, a foreign extension, two
// stems in one group — so an unrecognised file becomes a non-item rather than a
// misread one.
func detectMarkdownItem(dir string, src Source) (string, bool) {
	paths, err := src.List()
	if err != nil {
		return "", false
	}
	content := nonMetaPaths(paths)
	if len(content) == 0 {
		return "", false
	}
	stem := ""
	for _, p := range content {
		rel, ok := relToKind(dir, p)
		if !ok || strings.Contains(rel, "/") || path.Ext(rel) != ".md" {
			return "", false
		}
		if s := stemOf(p); stem == "" {
			stem = s
		} else if s != stem {
			return "", false
		}
	}
	if stem == "" {
		return "", false
	}
	return stem, true
}

// markdownForms reports the forms actually present.
//
// FormRaw is always FIRST because it is the BASE form: an unsuffixed filename
// belongs to it and "<stem>.distilled.md" belongs to FormDistilled. Both are
// PUBLISHED content — distilled/distilled_by are published bundle fields, not a
// local cache derivative — so both live in the content tree, both are hashed, and
// both are separately attestable. Reporting a form that has no file would hand a
// caller an empty Form to sign.
func markdownForms(dir string, src Source) ([]signing.Form, error) {
	stem, ok := detectMarkdownItem(dir, src)
	if !ok {
		return nil, fmt.Errorf("%w: not a %s item", ErrUnrecognized, dir)
	}
	paths, err := src.List()
	if err != nil {
		return nil, err
	}
	out := []signing.Form{signing.FormRaw}
	for _, p := range paths {
		if !IsMetaPath(p) && logicalBase(p) == stem+"."+string(signing.FormDistilled) {
			out = append(out, signing.FormDistilled)
			break
		}
	}
	return out, nil
}

// markdownParts is one .md kind's decoded files.
type markdownParts struct {
	stem      string
	raw       mdMeta
	rawBody   string
	distilled mdMeta
	distBody  string
	hasDist   bool
}

// readMarkdownItem decodes a .md candidate group.
//
// A metadata SIDECAR in a .md kind directory is refused rather than ignored: the
// residency rule for these kinds is front-matter, so a sidecar here is either a
// mistake or content smuggled past the decoder. It is grouped into the item (and
// therefore into the digest) by the walker, so silently ignoring it would mean
// bytes are attested that nothing can explain.
func readMarkdownItem(t SurfaceType, src Source) (markdownParts, error) {
	dir := t.Dir()
	var out markdownParts
	stem, ok := detectMarkdownItem(dir, src)
	if !ok {
		return out, fmt.Errorf("%w: not a %s item", ErrUnrecognized, dir)
	}
	out.stem = stem
	paths, err := src.List()
	if err != nil {
		return out, err
	}
	for _, p := range paths {
		if IsMetaPath(p) {
			if err := refuseUnexplainedMeta(t, stem, p); err != nil {
				return out, err
			}
		}
		data, err := src.Open(p)
		if err != nil {
			return out, err
		}
		fm, body, _ := splitFrontMatter(data)
		var meta mdMeta
		if err := unmarshalYAML(fm, &meta); err != nil {
			return out, fmt.Errorf("content: %s: %w", p, err)
		}
		if logicalBase(p) == stem+"."+string(signing.FormDistilled) {
			out.distilled, out.distBody, out.hasDist = meta, body, true
			continue
		}
		out.raw, out.rawBody = meta, body
	}
	return out, nil
}

// encodeMarkdownItem renders a .md kind's components.
func encodeMarkdownItem(dir, stem string, raw mdMeta, rawBody string, distilled mdMeta, distBody string, hasDist bool) ([]Component, error) {
	if stem == "" {
		return nil, fmt.Errorf("%w: surface has no name", ErrSurfaceType)
	}
	if strings.ContainsAny(stem, `/\`) {
		return nil, fmt.Errorf("%w: %s name %q must be a single path segment", ErrBadPath, dir, stem)
	}
	rawBytes, err := joinFrontMatter(raw, rawBody)
	if err != nil {
		return nil, err
	}
	out := []Component{{Path: itemPath(dir, stem, ".md"), Mode: ModeRegular, Bytes: rawBytes}}
	if hasDist {
		distBytes, err := joinFrontMatter(distilled, distBody)
		if err != nil {
			return nil, err
		}
		out = append(out, Component{
			Path:  itemPath(dir, stem+"."+string(signing.FormDistilled), ".md"),
			Mode:  ModeRegular,
			Bytes: distBytes,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------- fragments

// Fragment is a context fragment: prose injected into a session, optionally with
// a published distilled rewrite stored as a sibling file.
type Fragment struct {
	Name string
	Tags []string
	// Notes and Installation are human-facing and never sent to a model.
	Notes        string
	Installation string
	// ContentHash is the authored distillation change-detection field. It is
	// carried verbatim so the format is lossless; it is an INDEX and never a
	// trust input, and nothing in this package reads it.
	ContentHash string
	// Body is the raw authored prose, front-matter excluded.
	Body string
	// NoDistill suppresses distillation of this fragment.
	NoDistill bool
	// Distilled is the published distilled rewrite (empty when there is none),
	// stored in the FormDistilled sibling file.
	Distilled string
	// DistilledBy names the model that produced Distilled.
	DistilledBy string
}

func (Fragment) Kind() trust.ItemKind      { return trust.KindFragment }
func (Fragment) TrustKind() trust.ItemKind { return trust.KindFragment }

type fragmentType struct{}

func (fragmentType) Name() string { return trust.KindFragment.Dir() }
func (fragmentType) Dir() string  { return trust.KindFragment.Dir() }

// Meta: front-matter. Fragments are .md, so they follow the SKILL.md convention.
func (fragmentType) Meta() MetaStore { return InlineMeta{} }

func (t fragmentType) Detect(src Source) bool {
	_, ok := detectMarkdownItem(t.Dir(), src)
	return ok
}

func (t fragmentType) Forms(src Source) ([]signing.Form, error) {
	return markdownForms(t.Dir(), src)
}

func (t fragmentType) RefFor(bundle string, src Source) (trust.Ref, error) {
	stem, ok := detectMarkdownItem(t.Dir(), src)
	if !ok {
		return trust.Ref{}, fmt.Errorf("%w: not a fragment", ErrUnrecognized)
	}
	return trust.Ref{Bundle: bundle, Kind: trust.KindFragment, Name: stem}, nil
}

func (t fragmentType) Decode(src Source) (Surface, error) {
	parts, err := readMarkdownItem(t, src)
	if err != nil {
		return nil, err
	}
	return Fragment{
		Name:         parts.stem,
		Tags:         parts.raw.Tags,
		Notes:        parts.raw.Notes,
		Installation: parts.raw.Installation,
		ContentHash:  parts.raw.ContentHash,
		Body:         parts.rawBody,
		NoDistill:    parts.raw.NoDistill,
		Distilled:    parts.distBody,
		DistilledBy:  parts.distilled.DistilledBy,
	}, nil
}

func (t fragmentType) Encode(s Surface) ([]Component, error) {
	f, ok := s.(Fragment)
	if !ok {
		return nil, fmt.Errorf("%w: %T is not a Fragment", ErrSurfaceType, s)
	}
	raw := mdMeta{
		Tags:         f.Tags,
		Notes:        f.Notes,
		Installation: f.Installation,
		ContentHash:  f.ContentHash,
		NoDistill:    f.NoDistill,
	}
	hasDist := f.Distilled != ""
	return encodeMarkdownItem(t.Dir(), f.Name, raw, f.Body, mdMeta{DistilledBy: f.DistilledBy}, f.Distilled, hasDist)
}

// ----------------------------------------------------------------- commands

// Command is a user-invoked slash command. Its selector directory is "prompts"
// (trust.KindPrompt.Dir()); the kind is deliberately NOT the legacy "skills"
// value, which now means an Agent Skill package.
type Command struct {
	Name        string
	Description string
	Tags        []string
	// Notes is human-facing; Installation is sent to the model for commands.
	Notes        string
	Installation string
	// ContentHash is distillation change-detection bookkeeping, carried
	// verbatim and never read as a trust input.
	ContentHash string
	Body        string
	NoDistill   bool
	Distilled   string
	DistilledBy string
	// Exports is per-engine export settings, TYPED and readable — consumers need
	// these keys, so they are not an opaque passthrough.
	//
	// The type is defined in THIS package (see exports.go), not imported from
	// internal/bundles: importing bundles here would point the dependency the
	// wrong way and become a cycle once bundles consumes this package.
	Exports EngineExports
}

func (Command) Kind() trust.ItemKind      { return trust.KindPrompt }
func (Command) TrustKind() trust.ItemKind { return trust.KindPrompt }

type commandType struct{}

func (commandType) Name() string { return trust.KindPrompt.Dir() }
func (commandType) Dir() string  { return trust.KindPrompt.Dir() }

// Meta: front-matter, as for fragments.
func (commandType) Meta() MetaStore { return InlineMeta{} }

func (t commandType) Detect(src Source) bool {
	_, ok := detectMarkdownItem(t.Dir(), src)
	return ok
}

func (t commandType) Forms(src Source) ([]signing.Form, error) {
	return markdownForms(t.Dir(), src)
}

func (t commandType) RefFor(bundle string, src Source) (trust.Ref, error) {
	stem, ok := detectMarkdownItem(t.Dir(), src)
	if !ok {
		return trust.Ref{}, fmt.Errorf("%w: not a command", ErrUnrecognized)
	}
	return trust.Ref{Bundle: bundle, Kind: trust.KindPrompt, Name: stem}, nil
}

func (t commandType) Decode(src Source) (Surface, error) {
	parts, err := readMarkdownItem(t, src)
	if err != nil {
		return nil, err
	}
	return Command{
		Name:         parts.stem,
		Description:  parts.raw.Description,
		Tags:         parts.raw.Tags,
		Notes:        parts.raw.Notes,
		Installation: parts.raw.Installation,
		ContentHash:  parts.raw.ContentHash,
		Body:         parts.rawBody,
		NoDistill:    parts.raw.NoDistill,
		Distilled:    parts.distBody,
		DistilledBy:  parts.distilled.DistilledBy,
		Exports:      parts.raw.Exports,
	}, nil
}

func (t commandType) Encode(s Surface) ([]Component, error) {
	c, ok := s.(Command)
	if !ok {
		return nil, fmt.Errorf("%w: %T is not a Command", ErrSurfaceType, s)
	}
	raw := mdMeta{
		Description:  c.Description,
		Tags:         c.Tags,
		Notes:        c.Notes,
		Installation: c.Installation,
		ContentHash:  c.ContentHash,
		NoDistill:    c.NoDistill,
		Exports:      c.Exports,
	}
	hasDist := c.Distilled != ""
	return encodeMarkdownItem(t.Dir(), c.Name, raw, c.Body, mdMeta{DistilledBy: c.DistilledBy}, c.Distilled, hasDist)
}
