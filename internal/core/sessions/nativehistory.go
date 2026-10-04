package sessions

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// IsRealDir reports whether p is a directory itself — not a link, and not a
// Windows junction, which Lstat reports with a type bit beside ModeDir.
func IsRealDir(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().Type() == fs.ModeDir
}

// AdoptHistory moves every entry of the real history directory src into dst
// — a directory dst lacks moves whole, one it has is merged — then removes
// src, which is empty by then. Where both hold a file src's replaces dst's:
// a home's history started as native's copy and only grew. A move cut short
// leaves the rest in src, still a real directory, so the next adoption
// finishes it.
func AdoptHistory(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() && IsRealDir(to) {
			err = AdoptHistory(from, to)
		} else {
			err = safefs.Rename(afero.NewOsFs(), from, to)
		}
		if err != nil {
			return err
		}
	}
	return os.Remove(src)
}

// KeepHomeHistory adopts into native/ every history store an engine home in
// sessionDir holds as a real directory, so deleting the homes loses no
// history. native/<leaf>/ holds exactly each engine's history stores, so a
// real home/<leaf>/<store> beside an existing native/<leaf>/<store> is
// history; a store that is the link into native/ has nothing to move.
func KeepHomeHistory(sessionDir string) error {
	native := filepath.Join(sessionDir, paths.NativeDirName)
	leaves, err := os.ReadDir(native)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("keep home history: %w", err)
	}
	for _, leaf := range leaves {
		stores, err := os.ReadDir(filepath.Join(native, leaf.Name()))
		if err != nil {
			continue
		}
		for _, store := range stores {
			rel := filepath.Join(leaf.Name(), store.Name())
			home := filepath.Join(sessionDir, paths.SessionEngineHomesDirName, rel)
			if !store.IsDir() || !IsRealDir(home) {
				continue
			}
			if err := AdoptHistory(home, filepath.Join(native, rel)); err != nil {
				return fmt.Errorf("keep home history: move %s into native: %w", home, err)
			}
		}
	}
	return nil
}
