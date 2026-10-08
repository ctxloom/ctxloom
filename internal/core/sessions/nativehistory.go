package sessions

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// IsRealDir reports whether p is a directory itself on fsys — not a link,
// and not a Windows junction, which Lstat reports with a type bit beside
// ModeDir. A filesystem that cannot Lstat (afero.Lstater) answers from Stat,
// which on such a filesystem has no link to follow.
func IsRealDir(fsys afero.Fs, p string) bool {
	var (
		st  fs.FileInfo
		err error
	)
	if l, ok := fsys.(afero.Lstater); ok {
		st, _, err = l.LstatIfPossible(p)
	} else {
		st, err = fsys.Stat(p)
	}
	return err == nil && st.Mode().Type() == fs.ModeDir
}

// AdoptHistory moves every entry of the real history directory src into dst
// on fsys — a directory dst lacks moves whole, one it has is merged — then
// removes src, which is empty by then. Where both hold a file src's replaces
// dst's: a home's history started as native's copy and only grew. A move cut
// short leaves the rest in src, still a real directory, so the next adoption
// finishes it.
func AdoptHistory(fsys afero.Fs, src, dst string) error {
	entries, err := afero.ReadDir(fsys, src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() && IsRealDir(fsys, to) {
			err = AdoptHistory(fsys, from, to)
		} else {
			err = safefs.Rename(fsys, from, to)
		}
		if err != nil {
			return err
		}
	}
	return fsys.Remove(src)
}

// KeepHomeHistory adopts into native/ every history store an engine home in
// sessionDir on fsys holds as a real directory, so deleting the homes loses
// no history. native/<leaf>/ holds exactly each engine's history stores, so a
// real home/<leaf>/<store> beside an existing native/<leaf>/<store> is
// history; a store that is the link into native/ has nothing to move, and
// one nested below a further home var is reached through the directories on
// its way (keepStore).
func KeepHomeHistory(fsys afero.Fs, sessionDir string) error {
	native := filepath.Join(sessionDir, paths.NativeDirName)
	leaves, err := afero.ReadDir(fsys, native)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("keep home history: %w", err)
	}
	for _, leaf := range leaves {
		stores, err := afero.ReadDir(fsys, filepath.Join(native, leaf.Name()))
		if err != nil {
			continue
		}
		for _, store := range stores {
			rel := filepath.Join(leaf.Name(), store.Name())
			home := filepath.Join(sessionDir, paths.SessionEngineHomesDirName, rel)
			if !store.IsDir() || !IsRealDir(fsys, home) {
				continue
			}
			if err := keepStore(fsys, home, filepath.Join(native, rel)); err != nil {
				return fmt.Errorf("keep home history: move %s into native: %w", home, err)
			}
		}
	}
	return nil
}

// keepStore adopts the real directory home into its native counterpart —
// unless a store is nested below it and linked (linkedBelow): then home is
// only a directory on the way to that link, holding the session's other
// content, and only the real directories native/ mirrors are descended into.
//
// A nested store that is itself a real directory (a home whose links do not
// resolve, as in a container) has no link below to tell its path from its
// siblings', so the top-level directory is adopted whole, as a single-segment
// store is: nothing is lost, but the session's other content beside it moves
// too.
func keepStore(fsys afero.Fs, home, native string) error {
	if !linkedBelow(fsys, home, native) {
		return AdoptHistory(fsys, home, native)
	}
	entries, err := afero.ReadDir(fsys, native)
	if err != nil {
		return err
	}
	for _, e := range entries {
		h := filepath.Join(home, e.Name())
		if e.IsDir() && IsRealDir(fsys, h) {
			if err := keepStore(fsys, h, filepath.Join(native, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// linkedBelow reports whether, somewhere under the real directory home, the
// counterpart of a directory native/ holds is present but not a real
// directory: the history link into native/, nested below home.
func linkedBelow(fsys afero.Fs, home, native string) bool {
	entries, err := afero.ReadDir(fsys, native)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		h := filepath.Join(home, e.Name())
		switch {
		case IsRealDir(fsys, h):
			if linkedBelow(fsys, h, filepath.Join(native, e.Name())) {
				return true
			}
		case exists(fsys, h):
			return true
		}
	}
	return false
}

// exists reports whether anything, a link included, is at p on fsys.
func exists(fsys afero.Fs, p string) bool {
	if l, ok := fsys.(afero.Lstater); ok {
		_, _, err := l.LstatIfPossible(p)
		return err == nil
	}
	_, err := fsys.Stat(p)
	return err == nil
}
