package content

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// skillDescriptorName is the file an Agent Skill package must contain.
//
// It is used for recognition and for LAYOUT: it is the file an engine reads,
// so it is where Materialize places the selected body, and its form siblings
// (skillBodyName) are the package's other bodies. It is emphatically not a
// primary component: nothing designates it as "the" content of a skill for
// TRUST purposes, the digest covers every file of a form equally, and there is
// no Primary/Main/Descriptor concept anywhere in this package. Designating one
// file for identity would bake a naming convention into what is signed — the
// day a descriptor is renamed, what is signed would change silently.
const skillDescriptorName = "SKILL.md"

// skillBodyName is the package-relative file carrying a skill's body in form f:
// the descriptor itself for the base form, and the descriptor with the form
// suffix spliced before its extension otherwise ("SKILL.distilled.md"). That
// is the same filename convention formOf reads, so the store's per-form
// partition and Materialize's body selection name the same file.
func skillBodyName(f signing.Form) string {
	if f == signing.FormRaw {
		return skillDescriptorName
	}
	ext := path.Ext(skillDescriptorName)
	return strings.TrimSuffix(skillDescriptorName, ext) + "." + string(f) + ext
}

// skillBodyForms is every form a skill body can be authored in: the base form
// first, then each suffix form.
func skillBodyForms() []signing.Form {
	return append([]signing.Form{signing.FormRaw}, formSuffixForms...)
}

// SkillForms reports the forms a skill package carries, given its
// package-relative file paths: the base form always, then each suffix form
// whose body file is present. It is the rule skillType.Forms applies and is
// exported for a loader that holds a package as paths rather than as a Source.
func SkillForms(files []string) []signing.Form {
	out := []signing.Form{signing.FormRaw}
	for _, f := range formSuffixForms {
		if slices.Contains(files, skillBodyName(f)) {
			out = append(out, f)
		}
	}
	return out
}

// SkillMaterialization is what materializing a skill package for an engine in
// form f writes, as a map from each package-relative source path to the
// package-relative path it lands at. The selected body lands at the
// descriptor, because that is the one place an engine reads; every body of
// another form is absent from the map; every other file maps to itself.
//
// A form the package has no body for is ErrNoSuchForm, never an empty map and
// never a fallback to the body it does have: a caller that asked for a form
// gets that form or an error it cannot mistake for success.
func SkillMaterialization(files []string, f signing.Form) (map[string]string, error) {
	if !slices.Contains(SkillForms(files), f) {
		return nil, fmt.Errorf("%w: skill package has no %q body (has %v)", ErrNoSuchForm, f, SkillForms(files))
	}
	selected := skillBodyName(f)
	out := make(map[string]string, len(files))
	for _, p := range files {
		switch {
		case p == selected:
			out[p] = skillDescriptorName
		case slices.ContainsFunc(skillBodyForms(), func(other signing.Form) bool { return p == skillBodyName(other) }):
			continue
		default:
			out[p] = p
		}
	}
	return out, nil
}

// Skill is an Agent Skill package: a directory of files, plus ctxloom metadata
// held in a sidecar OUTSIDE the package so the package itself stays a pure Agent
// Skill tree with nothing of ours in it.
type Skill struct {
	Name  string
	Tags  []string
	Notes string
	// Exports is per-engine enablement. A skill's name and description are
	// SKILL.md front-matter — the single source of truth — and are never
	// duplicated here, so in practice only EngineExport.Enabled is meaningful for
	// a skill; the shared type carries the rest harmlessly.
	Exports EngineExports
	// Files is every file in the package, package-relative, sorted by path,
	// every body of every form included. No entry is privileged for trust;
	// Materialize is where one body is selected for an engine.
	Files []SkillFile
}

// Materialize returns the package as an engine receives it in form f: that
// form's body at the descriptor path, every other body omitted, every other
// file as-is, in path order. It applies SkillMaterialization, so selecting a
// form the package has no body for is ErrNoSuchForm and nothing is returned.
func (s Skill) Materialize(f signing.Form) ([]SkillFile, error) {
	paths := make([]string, len(s.Files))
	for i, file := range s.Files {
		paths[i] = file.Path
	}
	layout, err := SkillMaterialization(paths, f)
	if err != nil {
		return nil, fmt.Errorf("content: materializing skill %q: %w", s.Name, err)
	}
	out := make([]SkillFile, 0, len(layout))
	for _, file := range s.Files {
		target, ok := layout[file.Path]
		if !ok {
			continue
		}
		file.Path = target
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// SkillFile is one file of a skill package.
type SkillFile struct {
	// Path is relative to the package directory, e.g. "scripts/run.sh".
	Path string
	// Mode is the DECLARED mode. It comes from the sidecar's executable list,
	// not from the filesystem: a mode bit is not portable, so attesting a
	// declaration is what keeps the digest platform-independent.
	Mode  ComponentMode
	Bytes []byte
}

func (Skill) Kind() trust.ItemKind      { return trust.KindSkill }
func (Skill) TrustKind() trust.ItemKind { return trust.KindSkill }

// skillMeta is the sidecar shape.
type skillMeta struct {
	Tags    []string      `yaml:"tags,omitempty"`
	Notes   string        `yaml:"notes,omitempty"`
	Exports EngineExports `yaml:"exports,omitempty"`
	// Executable lists the package-relative paths whose exec bit is
	// load-bearing. This declaration is inside a hashed component, so
	// executability is ATTESTED — which is exactly the property that silently
	// disappears if a walker skips the dot-prefixed sidecar.
	Executable []string `yaml:"executable,omitempty"`
}

// DeclaredExecutable reports which files of a bundle tree the tree's OWN
// sidecars declare executable, given the tree as path-to-bytes.
//
// It exists for the one caller that holds a tree's bytes but cannot decode it
// as items: an INSTALLER. Writing a fetched tree to disk has to pick a POSIX
// mode per file, and the only legitimate answer is the declaration — a mode bit
// is not portable and the digest deliberately excludes it (see Digest), so the
// `executable:` list inside the hashed, signed sidecar is the whole of what a
// publisher said about executability. Taking the mode from the transport
// instead (git's 100755) produces a file whose mode disagrees with the manifest
// the same tree generates, which reads downstream as tampering.
//
// Keys are paths in the SAME space as the input map, and recognition is
// prefix-agnostic: a sidecar is any metadata path whose immediate parent
// directory is the skills directory, so both a single bundle's tree
// ("skills/.reviewer.meta.yaml") and a bundles root holding many
// ("atelier/skills/.reviewer.meta.yaml") resolve without the caller having to
// say which it handed over.
//
// Skills are the only kind that appears here because skills are the only kind
// whose item IS a directory of files; every other kind's component is a single
// document with no mode to declare.
func DeclaredExecutable(files map[string][]byte) (map[string]bool, error) {
	dir := trust.KindSkill.Dir()
	out := map[string]bool{}
	for p, data := range files {
		if !IsMetaPath(p) || path.Base(path.Dir(p)) != dir {
			continue
		}
		var meta skillMeta
		if err := unmarshalYAML(data, &meta); err != nil {
			return nil, fmt.Errorf("content: %s: %w", p, err)
		}
		pkg := path.Dir(p) + "/" + logicalBase(p)
		for _, rel := range meta.Executable {
			out[pkg+"/"+rel] = true
		}
	}
	return out, nil
}

type skillType struct{}

func (skillType) Name() string { return trust.KindSkill.Dir() }
func (skillType) Dir() string  { return trust.KindSkill.Dir() }

// Meta: a sidecar, placed BESIDE the package directory rather than inside it, so
// skills/<name>/ stays a pure Agent Skill tree with nothing of ours in it.
func (skillType) Meta() MetaStore { return SidecarMeta{} }

// detectSkill recognises a skill candidate: every non-sidecar component sits
// under one common package directory, and that directory holds a SKILL.md.
//
// Requiring the descriptor at the package root is what stops the walker's descent
// from inventing items: a directory below skills/ that has no SKILL.md is claimed
// by nobody, and recursing into it finds no SKILL.md at that level either, so a
// malformed package yields no item rather than a misnamed one.
func detectSkill(src Source) (string, bool) {
	dir := trust.KindSkill.Dir()
	paths, err := src.List()
	if err != nil {
		return "", false
	}
	content := nonMetaPaths(paths)
	if len(content) == 0 {
		return "", false
	}
	name := ""
	for _, p := range content {
		rel, ok := relToKind(dir, p)
		if !ok {
			return "", false
		}
		seg, rest, found := strings.Cut(rel, "/")
		if !found || seg == "" || rest == "" {
			return "", false
		}
		if name == "" {
			name = seg
		} else if seg != name {
			return "", false
		}
	}
	if !slices.Contains(content, dir+"/"+name+"/"+skillDescriptorName) {
		return "", false
	}
	return name, true
}

func (t skillType) Detect(src Source) bool {
	_, ok := detectSkill(src)
	return ok
}

// Forms reports the base form, then each suffix form whose body the package
// carries beside the descriptor (SkillForms). Only the descriptor carries a
// form: a sibling file whose name happens to end in a form suffix is content,
// not a body, and does not make the package claim a form it cannot materialize.
func (t skillType) Forms(src Source) ([]signing.Form, error) {
	name, ok := detectSkill(src)
	if !ok {
		return nil, fmt.Errorf("%w: not a skill package", ErrUnrecognized)
	}
	paths, err := src.List()
	if err != nil {
		return nil, err
	}
	prefix := t.Dir() + "/" + name + "/"
	rels := make([]string, 0, len(paths))
	for _, p := range paths {
		if rel, ok := strings.CutPrefix(p, prefix); ok {
			rels = append(rels, rel)
		}
	}
	return SkillForms(rels), nil
}

func (t skillType) RefFor(bundle string, src Source) (trust.Ref, error) {
	name, ok := detectSkill(src)
	if !ok {
		return trust.Ref{}, fmt.Errorf("%w: not a skill package", ErrUnrecognized)
	}
	return trust.Ref{Bundle: bundle, Kind: trust.KindSkill, Name: name}, nil
}

func (t skillType) Decode(src Source) (Surface, error) {
	name, ok := detectSkill(src)
	if !ok {
		return nil, fmt.Errorf("%w: not a skill package", ErrUnrecognized)
	}
	paths, err := src.List()
	if err != nil {
		return nil, err
	}
	var meta skillMeta
	prefix := t.Dir() + "/" + name + "/"
	files := make([]SkillFile, 0, len(paths))
	for _, p := range paths {
		data, err := src.Open(p)
		if err != nil {
			return nil, err
		}
		if IsMetaPath(p) {
			if err := unmarshalYAML(data, &meta); err != nil {
				return nil, fmt.Errorf("content: %s: %w", p, err)
			}
			continue
		}
		files = append(files, SkillFile{Path: strings.TrimPrefix(p, prefix), Bytes: data})
	}
	exec := meta.Executable
	for i := range files {
		files[i].Mode = ModeRegular
		if slices.Contains(exec, files[i].Path) {
			files[i].Mode = ModeExecutable
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return Skill{Name: name, Tags: meta.Tags, Notes: meta.Notes, Exports: meta.Exports, Files: files}, nil
}

func (t skillType) Encode(s Surface) ([]Component, error) {
	sk, ok := s.(Skill)
	if !ok {
		return nil, fmt.Errorf("%w: %T is not a Skill", ErrSurfaceType, s)
	}
	if sk.Name == "" {
		return nil, fmt.Errorf("%w: surface has no name", ErrSurfaceType)
	}
	if strings.ContainsAny(sk.Name, `/\`) {
		return nil, fmt.Errorf("%w: skill name %q must be a single path segment", ErrBadPath, sk.Name)
	}
	prefix := t.Dir() + "/" + sk.Name + "/"
	var exec []string
	var out []Component
	hasDescriptor := false
	for _, f := range sk.Files {
		if f.Path == "" || strings.HasPrefix(f.Path, "/") {
			return nil, fmt.Errorf("%w: skill file path %q", ErrBadPath, f.Path)
		}
		full := prefix + f.Path
		if err := validateDigestPath(full); err != nil {
			return nil, err
		}
		if f.Path == skillDescriptorName {
			hasDescriptor = true
		}
		if f.Mode == ModeExecutable {
			exec = append(exec, f.Path)
		}
		out = append(out, Component{Path: full, Mode: f.Mode, Bytes: f.Bytes})
	}
	if !hasDescriptor {
		return nil, fmt.Errorf("%w: skill %q has no %s", ErrSurfaceType, sk.Name, skillDescriptorName)
	}
	sort.Strings(exec)
	sidecar, err := marshalYAML(skillMeta{Tags: sk.Tags, Notes: sk.Notes, Exports: sk.Exports, Executable: exec})
	if err != nil {
		return nil, err
	}
	if len(sidecar) > 0 {
		metaPath, hasMeta := t.Meta().PathFor(t.Dir(), sk.Name)
		if !hasMeta {
			return nil, fmt.Errorf("%w: skills declare no metadata file", ErrSurfaceType)
		}
		out = append(out, Component{Path: metaPath, Mode: ModeRegular, Bytes: sidecar})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
