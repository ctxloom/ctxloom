//go:build integration || acceptance

package testenv

import (
	"fmt"
	"os"
	"path/filepath"
)

// SeedTreeRemote publishes a tree-form bundle — its bundle.yaml envelope and
// each item at its slash-separated path relative to the tree root (e.g.
// "fragments/marker.md") — into a seeded git remote. root is the
// remote-relative directory the tree lands in.
func (e *TestEnvironment) SeedTreeRemote(root, envelope string, items map[string]string) (string, error) {
	return e.SeedRemote(treeFiles(root, envelope, items))
}

// SeedLocalTree writes a tree-form bundle named bundleID under the project's
// own bundles root.
func (e *TestEnvironment) SeedLocalTree(bundleID, envelope string, items map[string]string) error {
	for rel, body := range treeFiles("", envelope, items) {
		if err := e.WriteFile(TreeBundleItemPath(bundleID, filepath.ToSlash(rel[1:])), body); err != nil {
			return err
		}
	}
	return nil
}

// AdvanceTreeRemote is SeedTreeRemote's AdvanceRemote counterpart: a REVISED
// tree, pushed as a second commit.
//
// Unlike AdvanceRemote (which only ever overlays the files it is given, so a
// path omitted from one round simply survives untouched from a previous one),
// this REPLACES the bundle's entire directory — the same "destination
// REPLACED, not merged" contract a pinned git worktree gives a real pulled
// tree. A caller renaming an item hands items a NEW path and expects the OLD
// one gone.
func (e *TestEnvironment) AdvanceTreeRemote(bareDir, root, envelope string, items map[string]string) error {
	work, err := cloneRemoteWork(e.Root, bareDir, "advance-tree-*")
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(work, filepath.FromSlash(root))); err != nil {
		return fmt.Errorf("clear the previous %s tree: %w", root, err)
	}
	if err := writeFilesUnder(work, treeFiles(root, envelope, items)); err != nil {
		return err
	}
	return runGitSteps(work, [][]string{
		{"add", "-A"},
		{"commit", "-m", "advance"},
		{"push", "origin", "main"},
	})
}

// treeFiles lays a tree out as a remote-relative files map under root.
func treeFiles(root, envelope string, items map[string]string) map[string]string {
	files := map[string]string{root + "/bundle.yaml": envelope}
	for rel, body := range items {
		files[root+"/"+rel] = body
	}
	return files
}
