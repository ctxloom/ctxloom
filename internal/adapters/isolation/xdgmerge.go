package isolation

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file builds a merged XDG tree (engine.HomeVar.Merge) inside a
// prepared session home: the owned dirs as the session's own, and on a host
// run every other top-level entry of the user's base linked in, with the
// same directory links (hostOS) isolation makes for the native history
// store. Nothing here names an engine or an app: the owned names are the
// engine's declaration.

// entryLinker is what a merge links through: the platform's directory links
// (hostOS). A test stands in a fake over an in-memory filesystem.
type entryLinker interface {
	LinkDir(link, target string) error
	LinkTarget(link string) (string, error)
	UnlinkDir(link string) error
}

// xdgBase is one merged var's tree to ready: dir is its directory in the
// session home; user is the user's own base to link from, "" for a run
// given none of the user's XDG content (a container).
type xdgBase struct {
	name  string
	dir   string
	user  string
	owns  []string
	files bool // the linker may link a non-directory (linksFiles)
}

// mergeXDGBases readies every merged var's tree in req's prepared home and
// returns what it skipped, each a line for the run's report. A host run
// (req.ShareUserXDG) links the user's base, resolved per the XDG Base
// Directory spec (engine.UserXDGBase); a container's tree holds the owned
// dirs alone.
func mergeXDGBases(req InstanceHomeRequest, vars []engine.HomeVar, ln entryLinker) ([]string, error) {
	var skipped []string
	for _, v := range vars {
		if v.Merge == nil {
			continue
		}
		b := xdgBase{name: v.Name, dir: filepath.Join(req.InstanceHome, filepath.FromSlash(v.Subdir)), owns: v.Merge.Owns, files: linksFiles}
		if req.ShareUserXDG {
			home, err := hostHomeDir()
			if err != nil {
				home = ""
			}
			user, ok := engine.UserXDGBase(v.Name, os.Getenv, home)
			if !ok {
				skipped = append(skipped, fmt.Sprintf("%s: the user's base cannot be resolved (no absolute $%s and no home directory); none of it is linked into %s", v.Name, v.Name, b.dir))
			}
			b.user = user
		}
		s, err := mergeBase(req.root(), ln, b)
		skipped = append(skipped, s...)
		if err != nil {
			return skipped, fmt.Errorf("instance home for %s: merged %s at %s: %w", req.Engine, v.Name, b.dir, err)
		}
	}
	return skipped, nil
}

// mergeBase readies one merged tree: the snapshot of the user's top-level
// entries (b.user's, or none) taken now, reconciled against what the tree
// already holds from an earlier run of the same session.
//
// The top level of a merged tree is the merge's: a link there that is not
// the link to the user's current entry of its name is removed (a container
// run removes every one, so it never even sees the names of the user's
// entries). A link is only ever unlinked, never followed. Each owned name is
// then a directory of the session's own, created owner-only; and each user
// entry that is not owned is linked in where the name is free. A name the
// session already holds for itself, an entry the platform cannot link, and
// a user base that does not exist are skipped, each reported.
func mergeBase(root safefs.Root, ln entryLinker, b xdgBase) ([]string, error) {
	var skipped []string
	snapshot, s := userEntries(root.Fs, b)
	skipped = append(skipped, s...)
	linked, err := reconcileLinks(root.Fs, ln, b, snapshot)
	if err != nil {
		return skipped, err
	}
	for _, o := range b.owns {
		if err := root.Private.Ensure(filepath.Join(b.dir, o)); err != nil {
			return skipped, fmt.Errorf("owned %s: %w", o, err)
		}
	}
	for _, n := range snapshot {
		if linked[n] || slices.Contains(b.owns, n) {
			continue
		}
		if why := linkEntry(root.Fs, ln, b, n); why != "" {
			skipped = append(skipped, fmt.Sprintf("%s: %s is not linked into the session's tree: %s", b.name, filepath.Join(b.user, n), why))
		}
	}
	return skipped, nil
}

// userEntries is the snapshot: the top-level names of the user's base,
// sorted; none for a run given no user base, or one whose base is missing,
// unreadable or overlaps the tree itself (each reported).
func userEntries(fsys afero.Fs, b xdgBase) ([]string, []string) {
	if b.user == "" {
		return nil, nil
	}
	if within(b.dir, b.user) || within(b.user, b.dir) {
		return nil, []string{fmt.Sprintf("%s: the user's base %s overlaps the session's tree %s; none of it is linked", b.name, b.user, b.dir)}
	}
	infos, err := afero.ReadDir(fsys, b.user)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, []string{fmt.Sprintf("%s: the user's base %s does not exist; nothing of it is linked", b.name, b.user)}
	case err != nil:
		return nil, []string{fmt.Sprintf("%s: the user's base %s cannot be read (%v); nothing of it is linked", b.name, b.user, err)}
	}
	names := make([]string, 0, len(infos))
	for _, fi := range infos {
		names = append(names, fi.Name())
	}
	slices.Sort(names)
	return names, nil
}

// reconcileLinks removes every top-level link in the tree that is not the
// link to the user's current entry of its name, and reports the names whose
// link stays.
func reconcileLinks(fsys afero.Fs, ln entryLinker, b xdgBase, snapshot []string) (map[string]bool, error) {
	kept := map[string]bool{}
	infos, err := afero.ReadDir(fsys, b.dir)
	if err != nil {
		return nil, err
	}
	for _, fi := range infos {
		n := fi.Name()
		link := filepath.Join(b.dir, n)
		target, err := ln.LinkTarget(link)
		if err != nil {
			continue // not a link: the session's own content, left as it is
		}
		if b.user != "" && target == filepath.Join(b.user, n) && slices.Contains(snapshot, n) && !slices.Contains(b.owns, n) {
			kept[n] = true
			continue
		}
		if err := ln.UnlinkDir(link); err != nil {
			return nil, fmt.Errorf("unlink %s: %w", link, err)
		}
	}
	return kept, nil
}

// linkEntry links the user's entry n into the tree, or says why it does not.
func linkEntry(fsys afero.Fs, ln entryLinker, b xdgBase, n string) string {
	link := filepath.Join(b.dir, n)
	if _, err := lstat(fsys, link); err == nil {
		return "the session holds its own " + n + " there"
	}
	target := filepath.Join(b.user, n)
	if fi, err := fsys.Stat(target); (err != nil || !fi.IsDir()) && !b.files {
		return "it is not a directory, and this platform's links join directories only"
	}
	if err := ln.LinkDir(link, target); err != nil {
		return err.Error()
	}
	return ""
}

// lstat is fsys's Lstat where it has one (a link is reported, not followed),
// else Stat.
func lstat(fsys afero.Fs, p string) (fs.FileInfo, error) {
	if l, ok := fsys.(afero.Lstater); ok {
		fi, _, err := l.LstatIfPossible(p)
		return fi, err
	}
	return fsys.Stat(p)
}

// within reports whether the clean host path p is root or beneath it.
func within(p, root string) bool {
	p, root = filepath.Clean(p), filepath.Clean(root)
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
