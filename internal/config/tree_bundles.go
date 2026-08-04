package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// treeBundleReaders resolves the entries the byte reader legitimately refused.
//
// remote.BundleReader serves SINGLE-FILE bundles and says so
// (ErrTreeBundleUnreadable) rather than hunting a "<name>.yaml" no publisher
// wrote. Those refusals are not failures — they are the tree-form entries, and
// this is where they get read, through a reader over the tree `remote pull`
// installed. Any OTHER failure is left in the map untouched for
// reportBundleLoadFailures.
//
// A tree whose directory cannot even be opened REPLACES the sentinel with its
// own error, so the user is told what actually went wrong instead of "the tree
// read path does not exist". A tree that opens but does not match what its
// publisher signed is the READER's answer, not this function's: it is a fact
// about bytes, established where the bytes are read.
func (c *Config) treeBundleReaders(lock *remote.Lockfile, root signing.TrustRoot, failures map[string]error) []bundles.Reader {
	var refused []string
	for canonical, err := range failures {
		if errors.Is(err, remote.ErrTreeBundleUnreadable) {
			refused = append(refused, canonical)
		}
	}
	sort.Strings(refused) // deterministic reader order across runs

	var out []bundles.Reader
	for _, canonical := range refused {
		entry, ok := lock.Bundles[canonical]
		if !ok {
			continue
		}
		reader, err := c.treeBundleReader(canonical, entry, root)
		if err != nil {
			failures[canonical] = err
			continue
		}
		out = append(out, reader)
		delete(failures, canonical)
	}
	return out
}

// treeBundleReader points a pinned-tree reader at the tree `remote pull`
// installed for one lockfile entry.
//
// WHY THE INSTALLED TREE AND NOT THE CLONE AT THE PINNED SHA — the single-file
// path reads its bytes back out of the git clone at entry.SHA, and the obvious
// symmetry would be to walk the tree there too. Two things rule it out:
//
//   - a bundle's SKILLS are files on disk. bundles.Bundle.FSDir has to return a
//     real directory or a skill package is unloadable (it refuses the synthetic
//     "<remote>:…" path outright), and the installed tree is the only real
//     directory a tree bundle has. That is what WithInstalledDir carries.
//   - verifying the installed tree is STRICTLY STRONGER than trusting the pin.
//     The reader checks the publisher's signature over the manifest AND the tree
//     against that manifest in both directions, so an edit to the installed
//     cache is caught. The pin cannot see the cache at all.
//
// The pin is not thereby abandoned: it is what `remote pull` fetched at, and it
// still decides WHICH bytes were installed. What changed is that integrity is
// now checked where the bytes are actually read from.
//
// The tree is rooted at the installed directory's PARENT: a bundle id must be a
// single path segment (content.validateBundleID), and a nested ref path
// ("lang/go/testing") is absorbed by the root rather than smuggled into the id.
func (c *Config) treeBundleReader(canonical string, entry remote.LockEntry, root signing.TrustRoot) (bundles.Reader, error) {
	if len(c.appPaths) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	dir, err := treeBundleDir(c.appPaths[0], canonical)
	if err != nil {
		return nil, err
	}
	tree, err := content.NewAferoTreeFS(c.getFS(), filepath.Dir(dir))
	if err != nil {
		return nil, fmt.Errorf("the lockfile records %q as a directory-form bundle but its tree is not installed at %s "+
			"(run `ctxloom remote pull`): %w", canonical, dir, err)
	}
	return bundles.NewRepoFSReader(tree, canonical,
		bundles.WithTrustRoot(root),
		bundles.WithInstalledDir(dir),
		bundles.WithRepoURL(entry.URL)), nil
}

// treeBundleDir resolves the directory `remote pull` installed a tree bundle
// into, from its canonical lockfile key. It goes through the same
// Reference.LocalTreePath the installer used rather than re-assembling the path,
// so a layout change cannot make the writer and the reader disagree.
func treeBundleDir(baseDir, canonical string) (string, error) {
	ref, err := remote.ParseReference(canonical)
	if err != nil {
		return "", fmt.Errorf("invalid lockfile bundle key %q: %w", canonical, err)
	}
	if !ref.IsCanonical() {
		return "", fmt.Errorf("invalid lockfile bundle key %q: not a canonical ref", canonical)
	}
	return ref.LocalTreePath(baseDir), nil
}
