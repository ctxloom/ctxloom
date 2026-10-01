// Package bundletree writes test fixtures as bundle TREES — the only form a
// bundle takes on disk — from a bundle value or its YAML spelling.
//
// A fixture states a bundle most readably as one YAML document, and that is
// how the suites have always written them. The document is only the fixture's
// NOTATION: nothing reads it back. Write parses it and lays it out the way an
// authored bundle is laid out, so the code under test reads a real tree.
//
// Fragments, commands, MCP servers and the envelope go through the production
// store's Save, so a fixture and a real edit produce the same bytes. Profiles,
// hooks and skills are written as tree items directly, because no production
// save writes them — the verbs that own them do.
package bundletree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// Write lays out the bundle doc spells as the tree <root>/<name>/ on fsys and
// returns the path of its envelope. name may be nested ("personal/foo").
func Write(t testing.TB, fsys afero.Fs, root, name, doc string, opts ...Option) string {
	t.Helper()
	envelope, err := WriteDoc(fsys, root, name, doc, opts...)
	require.NoError(t, err)
	return envelope
}

// WriteDoc is Write for a caller with no testing.TB (a godog step): it
// returns the failure instead of failing the test.
func WriteDoc(fsys afero.Fs, root, name, doc string, opts ...Option) (string, error) {
	b, err := bundles.ParseBundle([]byte(doc))
	if err != nil {
		return "", fmt.Errorf("bundletree: parsing the fixture document for %q: %w", name, err)
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if err := write(fsys, root, name, b, o); err != nil {
		return "", fmt.Errorf("bundletree: writing %q: %w", name, err)
	}
	return envelopePath(root, name), nil
}

func envelopePath(root, name string) string {
	return filepath.Join(root, filepath.FromSlash(name), bundles.DirectoryFormManifest)
}

// WriteOS is Write on the OS filesystem.
func WriteOS(t testing.TB, root, name, doc string, opts ...Option) string {
	t.Helper()
	return Write(t, afero.NewOsFs(), root, name, doc, opts...)
}

// Option adds to what WriteBundle writes.
type Option func(*options)

type options struct {
	skills map[string][]content.SkillFile
}

// File is one file of a skill package a fixture states.
type File struct {
	Body       string
	Executable bool
}

// WithSkill writes the skill package name — its files by package-relative
// path — with the metadata the bundle declares for it, if any.
func WithSkill(name string, files map[string]File) Option {
	return func(o *options) {
		if o.skills == nil {
			o.skills = map[string][]content.SkillFile{}
		}
		out := make([]content.SkillFile, 0, len(files))
		for _, p := range collections.SortedKeys(files) {
			mode := content.ModeRegular
			if files[p].Executable {
				mode = content.ModeExecutable
			}
			out = append(out, content.SkillFile{Path: p, Mode: mode, Bytes: []byte(files[p].Body)})
		}
		o.skills[name] = out
	}
}

// WriteBundle lays out b as the tree <root>/<name>/ on fsys and returns the
// path of its envelope. b is not modified.
//
// A skill's package is stated with WithSkill, or is a directory the fixture
// writes itself (skills/<skill>/SKILL.md and siblings). A skill b declares
// with metadata — tags, notes or exports — and no WithSkill is read from the
// files already in place.
func WriteBundle(t testing.TB, fsys afero.Fs, root, name string, b *bundles.Bundle, opts ...Option) string {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	require.NoError(t, write(fsys, root, name, b, o), "bundletree: writing %q", name)
	return envelopePath(root, name)
}

func write(fsys afero.Fs, root, name string, b *bundles.Bundle, o options) error {
	envelope := envelopePath(root, name)
	if err := saveEnvelope(fsys, envelope, b); err != nil {
		return err
	}
	dir := filepath.Dir(envelope)
	w, err := content.NewTreeStore(fsys, filepath.Dir(dir), content.Provenance{IsLocal: true})
	if err != nil {
		return err
	}
	tw := treeWriter{ctx: context.Background(), w: w, fsys: fsys, dir: dir, id: filepath.Base(dir)}
	if err := tw.putProfiles(b); err != nil {
		return err
	}
	if err := tw.putHooks(b); err != nil {
		return err
	}
	return tw.putSkills(b, o)
}

// saveEnvelope writes b's envelope — its bundle-level fields, none of its
// items — at envelope.
//
// A tree's envelope carries no items, so a fixture document that declared
// only items would leave an envelope declaring nothing, which ParseBundle
// refuses. A fixture that states its own version keeps it.
func saveEnvelope(fsys afero.Fs, envelope string, b *bundles.Bundle) error {
	core := *b
	core.Path = envelope
	if core.Version == "" {
		core.Version = "1.0.0"
	}
	core.Profiles, core.Hooks, core.Skills = nil, bundles.BundleHooks{}, nil
	return bundles.NewFSStore(fsys, nil).Save(&core)
}

// treeWriter puts one bundle's items into its tree.
type treeWriter struct {
	ctx  context.Context
	w    *content.TreeStore
	fsys afero.Fs
	dir  string
	id   string
}

// put writes one item, raw.
func (tw treeWriter) put(kind trust.ItemKind, item string, s content.Surface) error {
	return tw.w.Put(tw.ctx, trust.Ref{Bundle: tw.id, Kind: kind, Name: item}, signing.FormRaw, s)
}

// putProfiles writes b's profiles, in name order.
func (tw treeWriter) putProfiles(b *bundles.Bundle) error {
	for _, p := range collections.SortedKeys(b.Profiles) {
		if err := tw.put(content.KindProfile, p, content.Profile{Name: p, Def: b.Profiles[p]}); err != nil {
			return fmt.Errorf("profile %q: %w", p, err)
		}
	}
	return nil
}

// putHooks writes b's hooks, each named and ordered by its position unless
// it states an order.
func (tw treeWriter) putHooks(b *bundles.Bundle) error {
	for _, e := range b.Hooks.Entries() {
		h := e.Hook
		order := (e.Index + 1) * content.HookOrderStep
		if h.Order != nil {
			order = *h.Order
		}
		hookName := fmt.Sprintf("hook-%d", e.Index+1)
		if err := tw.put(trust.KindHook, e.Event+"/"+hookName, content.Hook{
			Event: e.Event, Name: hookName, Order: &order,
			Matcher: h.Matcher, Type: h.Type, Command: h.Command, Args: h.Args, Prompt: h.Prompt,
			Timeout: h.Timeout, Async: h.Async, PreToolFallback: h.PreToolFallback,
		}); err != nil {
			return fmt.Errorf("hook %s[%d]: %w", e.Event, e.Index, err)
		}
	}
	return nil
}

// putSkills writes every skill b declares or WithSkill states, in name
// order (declared first, then stated-only).
func (tw treeWriter) putSkills(b *bundles.Bundle, o options) error {
	skillNames := collections.SortedKeys(b.Skills)
	for _, s := range collections.SortedKeys(o.skills) {
		if _, declared := b.Skills[s]; !declared {
			skillNames = append(skillNames, s)
		}
	}
	for _, s := range skillNames {
		if err := tw.putSkill(s, b.Skills[s], o); err != nil {
			return fmt.Errorf("skill %q: %w", s, err)
		}
	}
	return nil
}

// putSkill writes one skill: its stated package, else — when b declares it
// with metadata — the files already in place; a skill with neither is
// skipped.
func (tw treeWriter) putSkill(s string, sk bundles.BundleSkill, o options) error {
	files, stated := o.skills[s]
	if !stated {
		if len(sk.Tags) == 0 && sk.Notes == "" && len(sk.Exports) == 0 {
			return nil
		}
		var err error
		if files, err = skillFiles(tw.fsys, filepath.Join(tw.dir, "skills", s)); err != nil {
			return err
		}
	}
	exports, err := bundles.TreeExports(sk.Exports)
	if err != nil {
		return err
	}
	return tw.put(trust.KindSkill, s, content.Skill{Name: s, Tags: sk.Tags, Notes: sk.Notes, Exports: exports, Files: files})
}

// skillFiles reads the package the fixture already wrote at dir.
func skillFiles(fsys afero.Fs, dir string) ([]content.SkillFile, error) {
	var out []content.SkillFile
	err := afero.Walk(fsys, dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := afero.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		mode := content.ModeRegular
		if info.Mode().Perm()&0o111 != 0 {
			mode = content.ModeExecutable
		}
		out = append(out, content.SkillFile{Path: filepath.ToSlash(rel), Mode: mode, Bytes: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("a skill with metadata needs its files written first: %w", err)
	}
	return out, nil
}
