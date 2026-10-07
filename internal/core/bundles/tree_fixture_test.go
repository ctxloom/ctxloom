package bundles

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// writeTree lays out the bundle doc spells as the tree <root>/<name>/ and
// returns its envelope path. It is this package's copy of
// internal/testsupport/bundletree.Write, which these in-package tests cannot
// import: that package imports this one.
func writeTree(t testing.TB, fsys afero.Fs, root, name, doc string) string {
	t.Helper()
	b, err := ParseBundle([]byte(doc))
	require.NoError(t, err)
	envelope := filepath.Join(root, filepath.FromSlash(name), DirectoryFormManifest)
	core := *b
	core.Path = envelope
	if core.Version == "" {
		core.Version = "1.0.0"
	}
	core.Profiles, core.Hooks, core.Skills = nil, BundleHooks{}, nil
	require.NoError(t, NewFSStore(fsys, nil).Save(&core))

	dir := filepath.Dir(envelope)
	w, err := content.NewTreeStore(fsys, filepath.Dir(dir), content.Provenance{IsLocal: true})
	require.NoError(t, err)
	put := func(kind ident.ItemKind, item string, s content.Surface) {
		require.NoError(t, w.Put(context.Background(), ident.Ref{Bundle: filepath.Base(dir), Kind: kind, Name: item}, ident.FormRaw, s))
	}
	for _, p := range collections.SortedKeys(b.Profiles) {
		put(content.KindProfile, p, content.Profile{Name: p, Def: b.Profiles[p]})
	}
	for _, e := range b.Hooks.Entries() {
		h := e.Hook
		order := (e.Index + 1) * content.HookOrderStep
		if h.Order != nil {
			order = *h.Order
		}
		h.Order = &order
		hookName := fmt.Sprintf("hook-%d", e.Index+1)
		put(ident.KindHook, e.Event+"/"+hookName, TreeHook(e.Event, hookName, h))
	}
	for _, name := range collections.SortedKeys(b.Skills) {
		sk := b.Skills[name]
		require.Emptyf(t, sk.Path, "skill %q declares a path, which a tree cannot express: its package is skills/%s", name, name)
		if len(sk.Tags) == 0 && sk.Notes == "" && len(sk.Exports) == 0 {
			continue
		}
		exports, err := TreeExports(sk.Exports)
		require.NoError(t, err)
		put(ident.KindSkill, name, content.Skill{Name: name, Tags: sk.Tags, Notes: sk.Notes, Exports: exports,
			Files: stagedSkillFiles(t, fsys, filepath.Join(dir, "skills", name))})
	}
	return envelope
}

// stagedSkillFiles reads the package a fixture already wrote at dir.
func stagedSkillFiles(t testing.TB, fsys afero.Fs, dir string) []content.SkillFile {
	t.Helper()
	var out []content.SkillFile
	require.NoError(t, afero.Walk(fsys, dir, func(p string, info os.FileInfo, err error) error {
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
	}), "a skill with metadata needs its files written first")
	return out
}
