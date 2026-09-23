package bundles

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// The WRITE direction of the tree mapping: a bundle item becomes the content
// surface its tree file encodes. It sits beside tree_read.go's inverse so a
// field added to one direction and forgotten in the other is a round-trip
// failure in one package, and it is the ONE forward mapping: the migration
// converter (internal/adapters/content/convert) and fsStore.Save both call it.

// TreeFragment is a fragment's tree surface. A fragment's premise is the tree
// format's `description`, as it already is for commands and skills.
func TreeFragment(name string, f BundleFragment) content.Fragment {
	return content.Fragment{Name: name, ItemMeta: treeItemMeta(f.ItemBody, f.Premise)}
}

// TreeCommand is a command's tree surface.
func TreeCommand(name string, c BundleCommand) (content.Command, error) {
	exports, err := TreeExports(c.Exports)
	if err != nil {
		return content.Command{}, fmt.Errorf("command %q: %w", name, err)
	}
	return content.Command{Name: name, ItemMeta: treeItemMeta(c.ItemBody, c.Description), Exports: exports}, nil
}

// TreeMCP is an MCP server's tree surface.
func TreeMCP(name string, m BundleMCP) content.MCP {
	return content.MCP{
		Name:         name,
		Command:      m.Command,
		Args:         m.Args,
		Env:          m.Env,
		ServedBy:     m.ServedBy,
		Notes:        m.Notes,
		Installation: m.Installation,
		ContentHash:  m.ContentHash,
	}
}

// TreeExports maps the opaque per-engine blocks onto the tree form's, block
// for block — the inverse of engineBlocks. nil when the item declares none.
func TreeExports(blocks EngineBlocks) (content.EngineExports, error) {
	if len(blocks) == 0 {
		return nil, nil
	}
	out := make(content.EngineExports, len(blocks))
	for engine, raw := range blocks {
		var block content.EngineExport
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, fmt.Errorf("exports: engine %q: %w", engine, err)
		}
		out[engine] = block
	}
	return out, nil
}

// treeItemMeta is the inverse of itemBody.
func treeItemMeta(body ItemBody, description string) content.ItemMeta {
	return content.ItemMeta{
		Tags:         body.Tags,
		Description:  description,
		Notes:        body.Notes,
		Installation: body.Installation,
		ContentHash:  body.ContentHash,
		Body:         body.Content,
		NoDistill:    body.NoDistill,
		Distilled:    body.Distilled,
		DistilledBy:  body.DistilledBy,
	}
}

// TreeEnvelope renders the bundle-level document a tree carries at
// bundle.yaml: b with every ITEM map cleared, so the tree has exactly one
// answer for each item — its file. Clearing rather than copying the metadata
// fields means a field added to Bundle travels without a list to update.
func TreeEnvelope(b *Bundle) ([]byte, error) {
	env := *b
	env.Fragments = nil
	env.Commands = nil
	env.MCP = nil
	env.Skills = nil
	env.Profiles = nil
	env.Hooks = BundleHooks{}
	raw, err := yaml.Marshal(&env)
	if err != nil {
		return nil, fmt.Errorf("bundles: rendering the %s envelope: %w", DirectoryFormManifest, err)
	}
	return raw, nil
}

// saveTree writes b back into the TREE whose envelope is at b.Path, touching
// only what changed. Rendering the whole bundle into bundle.yaml — the document
// save — would declare every item inline, which turns the tree into a document
// in a directory: its item files are then silently ignored by the reader
// (treeFormEnvelope) while still sitting on disk looking authoritative.
//
// The current tree is re-read from disk and diffed item by item, so an item
// that did not change is not rewritten and its bytes — and any approval over
// them — are untouched. Kinds whose items only other verbs write (skills,
// hooks, profiles) are refused if they differ rather than rewritten: a save
// that could only approximate them would change what they do.
func (s *fsStore) saveTree(ctx context.Context, b *Bundle) error {
	tree, err := openTreeAt(ctx, s.fs, b.Path, content.Provenance{IsLocal: true})
	if err != nil {
		return err
	}
	cur, err := ReadTree(ctx, tree)
	if err != nil {
		return err
	}
	for kind, same := range map[string]bool{
		"skills":   reflect.DeepEqual(cur.Skills, b.Skills),
		"hooks":    reflect.DeepEqual(cur.Hooks, b.Hooks),
		"profiles": reflect.DeepEqual(cur.Profiles, b.Profiles),
	} {
		if !same {
			return fmt.Errorf("bundles: saving tree bundle %s: its %s changed, and an item save cannot write them; use the verb that owns them", b.Path, kind)
		}
	}
	dir := filepath.Dir(b.Path)
	id := filepath.Base(dir)
	w, err := content.NewTreeStore(s.fs, filepath.Dir(dir), content.Provenance{IsLocal: true})
	if err != nil {
		return fmt.Errorf("bundles: opening the tree at %s: %w", dir, err)
	}
	t := treeSave{ctx: ctx, w: w, bundle: id}
	if err := saveItems(t, trust.KindFragment, cur.Fragments, b.Fragments, func(n string, f BundleFragment) (content.Surface, string, error) {
		return TreeFragment(n, f), f.Distilled, nil
	}); err != nil {
		return err
	}
	if err := saveItems(t, trust.KindPrompt, cur.Commands, b.Commands, func(n string, c BundleCommand) (content.Surface, string, error) {
		s, err := TreeCommand(n, c)
		return s, c.Distilled, err
	}); err != nil {
		return err
	}
	if err := saveItems(t, trust.KindMCP, cur.MCP, b.MCP, func(n string, m BundleMCP) (content.Surface, string, error) {
		return TreeMCP(n, m), "", nil
	}); err != nil {
		return err
	}
	return s.saveTreeEnvelope(ctx, w, id, b)
}

type treeSave struct {
	ctx    context.Context
	w      *content.TreeStore
	bundle string
}

// saveItems writes the items of one kind that differ between cur (on disk)
// and next, and deletes the ones next no longer holds. An item that lost its
// distilled form is deleted first, so a stale distilled file cannot outlive
// the content it summarised.
func saveItems[T any](t treeSave, kind trust.ItemKind, cur, next map[string]T, surface func(string, T) (content.Surface, string, error)) error {
	for _, name := range collections.SortedKeys(cur) {
		if _, ok := next[name]; !ok {
			if err := t.w.Delete(t.ctx, trust.Ref{Bundle: t.bundle, Kind: kind, Name: name}); err != nil {
				return fmt.Errorf("bundles: removing %s %q: %w", kind, name, err)
			}
		}
	}
	for _, name := range collections.SortedKeys(next) {
		was, had := cur[name]
		if had && reflect.DeepEqual(was, next[name]) {
			continue
		}
		ref := trust.Ref{Bundle: t.bundle, Kind: kind, Name: name}
		s, distilled, err := surface(name, next[name])
		if err != nil {
			return err
		}
		if had {
			if _, wasDistilled, _ := surface(name, was); wasDistilled != "" && distilled == "" {
				if err := t.w.Delete(t.ctx, ref); err != nil {
					return fmt.Errorf("bundles: clearing %s: %w", ref.Key(), err)
				}
			}
		}
		if err := t.w.Put(t.ctx, ref, signing.FormRaw, s); err != nil {
			return fmt.Errorf("bundles: writing %s: %w", ref.Key(), err)
		}
		if distilled != "" {
			if err := t.w.Put(t.ctx, ref, signing.FormDistilled, s); err != nil {
				return fmt.Errorf("bundles: writing %s (distilled): %w", ref.Key(), err)
			}
		}
	}
	return nil
}

// saveTreeEnvelope rewrites bundle.yaml only when the bundle-level fields
// changed. The name a reader derives from the location when the envelope
// declares none is not a declaration, so it is not written back as one.
func (s *fsStore) saveTreeEnvelope(ctx context.Context, w *content.TreeStore, id string, b *Bundle) error {
	_, onDisk, err := EnvelopeAt(s.fs, b.Path)
	if err != nil {
		return err
	}
	next := *b
	if onDisk.Name == "" && next.Name == ExtractBundleName(b.Path) {
		next.Name = ""
	}
	want, err := TreeEnvelope(&next)
	if err != nil {
		return err
	}
	have, err := TreeEnvelope(onDisk)
	if err != nil {
		return err
	}
	if string(want) == string(have) {
		return nil
	}
	if err := w.PutRootFile(ctx, content.BundleID(id), DirectoryFormManifest, want); err != nil {
		return fmt.Errorf("bundles: writing the envelope at %s: %w", b.Path, err)
	}
	return nil
}
