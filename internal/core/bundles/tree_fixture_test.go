package bundles

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/trust"
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
	core.Profiles, core.Hooks, core.Skills = nil, BundleHooks{}, nil
	require.NoError(t, NewFSStore(fsys, nil).Save(&core))

	dir := filepath.Dir(envelope)
	w, err := content.NewTreeStore(fsys, filepath.Dir(dir), content.Provenance{IsLocal: true})
	require.NoError(t, err)
	put := func(kind trust.ItemKind, item string, s content.Surface) {
		require.NoError(t, w.Put(context.Background(), trust.Ref{Bundle: filepath.Base(dir), Kind: kind, Name: item}, signing.FormRaw, s))
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
		hookName := fmt.Sprintf("hook-%d", e.Index+1)
		put(trust.KindHook, e.Event+"/"+hookName, content.Hook{
			Event: e.Event, Name: hookName, Order: &order,
			Matcher: h.Matcher, Type: h.Type, Command: h.Command, Prompt: h.Prompt,
			Timeout: h.Timeout, Async: h.Async, PreToolFallback: h.PreToolFallback,
		})
	}
	return envelope
}
