package content

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// skillDescriptorName is the file an Agent Skill package must contain.
//
// It is used ONLY for recognition. It is emphatically not a primary component:
// nothing designates it as "the" content of a skill, the digest covers every file
// equally, and there is no Primary/Main/Descriptor concept anywhere in this
// package. Designating one file would bake a naming convention into trust
// identity — the day a skill grows a second entry point, what is signed would
// change silently.
const skillDescriptorName = "SKILL.md"

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
	// Files is every file in the package, package-relative, sorted by path. No
	// entry is privileged, SKILL.md included.
	Files []SkillFile
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

// Forms reports FormRaw only. A skill is never distilled: SKILL.md's description
// IS the progressive-disclosure mechanism a model reads before loading the rest,
// and distilling that would defeat it.
func (t skillType) Forms(src Source) ([]signing.Form, error) {
	if _, ok := detectSkill(src); !ok {
		return nil, fmt.Errorf("%w: not a skill package", ErrUnrecognized)
	}
	return []signing.Form{signing.FormRaw}, nil
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
