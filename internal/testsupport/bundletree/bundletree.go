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
func Write(t testing.TB, fsys afero.Fs, root, name, doc string) string {
	t.Helper()
	b, err := bundles.ParseBundle([]byte(doc))
	require.NoError(t, err, "bundletree: parsing the fixture document for %q", name)
	return WriteBundle(t, fsys, root, name, b)
}

// WriteOS is Write on the OS filesystem.
func WriteOS(t testing.TB, root, name, doc string) string {
	t.Helper()
	return Write(t, afero.NewOsFs(), root, name, doc)
}

// WriteBundle lays out b as the tree <root>/<name>/ on fsys and returns the
// path of its envelope. b is not modified.
//
// A skill b declares is a directory the fixture writes itself
// (skills/<skill>/SKILL.md and siblings), before or after this call. Only a
// skill that carries metadata — tags, notes or exports — needs this call to
// write it, and then its files must already be in place.
func WriteBundle(t testing.TB, fsys afero.Fs, root, name string, b *bundles.Bundle) string {
	t.Helper()
	require.NoError(t, write(fsys, root, name, b), "bundletree: writing %q", name)
	return filepath.Join(root, filepath.FromSlash(name), bundles.DirectoryFormManifest)
}

func write(fsys afero.Fs, root, name string, b *bundles.Bundle) error {
	ctx := context.Background()
	envelope := filepath.Join(root, filepath.FromSlash(name), bundles.DirectoryFormManifest)
	core := *b
	core.Path = envelope
	core.Profiles, core.Hooks, core.Skills = nil, bundles.BundleHooks{}, nil
	if err := bundles.NewFSStore(fsys, nil).Save(&core); err != nil {
		return err
	}

	dir := filepath.Dir(envelope)
	id := filepath.Base(dir)
	w, err := content.NewTreeStore(fsys, filepath.Dir(dir), content.Provenance{IsLocal: true})
	if err != nil {
		return err
	}
	put := func(kind trust.ItemKind, item string, s content.Surface) error {
		return w.Put(ctx, trust.Ref{Bundle: id, Kind: kind, Name: item}, signing.FormRaw, s)
	}
	for _, p := range collections.SortedKeys(b.Profiles) {
		if err := put(content.KindProfile, p, content.Profile{Name: p, Def: b.Profiles[p]}); err != nil {
			return fmt.Errorf("profile %q: %w", p, err)
		}
	}
	for _, e := range b.Hooks.Entries() {
		h := e.Hook
		order := (e.Index + 1) * content.HookOrderStep
		if h.Order != nil {
			order = *h.Order
		}
		hookName := fmt.Sprintf("hook-%d", e.Index+1)
		if err := put(trust.KindHook, e.Event+"/"+hookName, content.Hook{
			Event: e.Event, Name: hookName, Order: &order,
			Matcher: h.Matcher, Type: h.Type, Command: h.Command, Prompt: h.Prompt,
			Timeout: h.Timeout, Async: h.Async, PreToolFallback: h.PreToolFallback,
		}); err != nil {
			return fmt.Errorf("hook %s[%d]: %w", e.Event, e.Index, err)
		}
	}
	for _, s := range collections.SortedKeys(b.Skills) {
		sk := b.Skills[s]
		if len(sk.Tags) == 0 && sk.Notes == "" && len(sk.Exports) == 0 {
			continue
		}
		files, err := skillFiles(fsys, filepath.Join(dir, "skills", s))
		if err != nil {
			return fmt.Errorf("skill %q: %w", s, err)
		}
		exports, err := bundles.TreeExports(sk.Exports)
		if err != nil {
			return fmt.Errorf("skill %q: %w", s, err)
		}
		if err := put(trust.KindSkill, s, content.Skill{Name: s, Tags: sk.Tags, Notes: sk.Notes, Exports: exports, Files: files}); err != nil {
			return fmt.Errorf("skill %q: %w", s, err)
		}
	}
	return nil
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
