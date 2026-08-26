package config

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// treeBundleReaders builds one reader per DIRECTORY-form lockfile entry, over
// the tree `deps pull` installed.
//
// The set comes from the lockfile's own Tree flag — the recorded FACT about how
// each bundle was published — and deliberately NOT from which reads the byte
// source refused. A refusal is a property of how that source happens to be
// wired: remote.WithReaderTreeFetcher makes the same entry readable, and a
// caller that wired one would silently empty this set and re-present every
// skill bundle as a single document, losing its skills. Dispatching on the fact
// cannot be turned off by wiring.
//
// Any byte-source failure recorded against a tree entry is therefore CLEARED
// once its reader is built: it described a road not taken. Failures for other
// entries are left untouched for reportBundleLoadFailures.
//
// A tree whose directory cannot even be opened REPLACES that entry's failure
// with its own, so the user is told what actually went wrong. A tree that opens
// but does not match what its publisher signed is the READER's answer, not this
// function's: it is a fact about bytes, established where the bytes are read.
func (c *Config) treeBundleReaders(lock *remote.Lockfile, root signing.TrustRoot, failures map[string]error) []bundles.Reader {
	var trees []string
	for canonical, entry := range lock.Bundles {
		if entry.Tree {
			trees = append(trees, canonical)
		}
	}
	sort.Strings(trees) // deterministic reader order across runs

	var out []bundles.Reader
	for _, canonical := range trees {
		reader, err := c.treeBundleReader(canonical, lock.Bundles[canonical], root)
		if err != nil {
			failures[canonical] = err
			continue
		}
		out = append(out, reader)
		delete(failures, canonical)
	}
	return out
}

// treeBundleReader points a pinned-tree reader at the tree `deps pull`
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
// The pin is not thereby abandoned: it is what `deps pull` fetched at, and it
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
	// Check the installed tree is THERE before handing a reader a root it will
	// fail to list later. The two failures are the same fact, but only here is
	// the fix knowable: a lockfile entry with no tree on disk is a pull that has
	// not happened, and the message has to say so — "cannot list this directory"
	// reaches the user as a bug in ctxloom.
	if ok, derr := afero.DirExists(c.getFS(), dir); derr != nil || !ok {
		return nil, fmt.Errorf("the lockfile records %q as a directory-form bundle but its tree is not installed at %s "+
			"(run `ctxloom deps pull`)", canonical, dir)
	}
	tree, err := content.NewAferoTreeFS(c.getFS(), filepath.Dir(dir))
	if err != nil {
		return nil, fmt.Errorf("the tree installed for %q at %s cannot be opened: %w", canonical, dir, err)
	}
	return bundles.NewRepoFSReader(tree, canonical,
		bundles.WithTrustRoot(root),
		bundles.WithInstalledDir(dir),
		bundles.WithPinnedRevision(entry.SHA),
		bundles.WithRepoURL(entry.URL)), nil
}

// treeBundleDir resolves the directory `deps pull` installed a tree bundle
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
