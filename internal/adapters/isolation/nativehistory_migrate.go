package isolation

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The two halves of a runtime switch on a host whose directory links do not
// resolve inside a container (platform.DirLinker.LinksResolveInContainers):
// a host run keeps history in native/ behind a link, a container run keeps it
// as a real directory in the mounted home. Each run converts what the
// previous one left, and neither direction may lose history or refuse.

// historyFs is the filesystem history is migrated on.
var historyFs = afero.NewOsFs()

// isRealDir reports whether p is a directory itself — not a link, and not a
// junction, which Lstat reports with a type bit beside ModeDir.
func isRealDir(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().Type() == fs.ModeDir
}

// adoptHistory moves every entry of the real history directory src into dst
// — a directory dst lacks moves whole, one it has is merged — then removes
// src, which is empty by then, ready to be linked. Where both hold a file
// src's replaces dst's: a home's copy started as native's
// (restoreNativeHistory) and only grew. A move cut short leaves the rest in
// src, still a real directory, so the next host run finishes it.
func adoptHistory(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() && isRealDir(to) {
			err = adoptHistory(from, to)
		} else {
			err = safefs.Rename(historyFs, from, to)
		}
		if err != nil {
			return err
		}
	}
	return os.Remove(src)
}

// restoreNativeHistory readies <instanceHome>/<rel> as a real history
// directory holding <nativeHome>/<rel>'s history, for an engine that cannot
// follow a link into native/. A real directory already there is the history
// of the run before, kept as it is. Otherwise native's history is copied
// into a staging directory beside it, the link (if any) removed through the
// platform (platform.DirLinker.UnlinkDir), and the copy renamed into place:
// the home never holds a partial copy, and native/ keeps its own.
func restoreNativeHistory(instanceHome, nativeHome, rel string) error {
	link := filepath.Join(instanceHome, filepath.FromSlash(rel))
	target := filepath.Join(nativeHome, filepath.FromSlash(rel))
	at, err := historyAt(link, nativeHome, rel)
	if err != nil || at == historyRealDir {
		return err
	}
	linked := at == historyLinked || at == historyLinkedBeforeRename
	if !isRealDir(target) {
		return unlinkIf(linked, link)
	}
	return swapInCopy(target, link, linked)
}

// swapInCopy copies target into a staging directory beside link, removes the
// link when there is one, and renames the copy to link.
func swapInCopy(target, link string, linked bool) error {
	staged := link + ".restoring"
	if err := os.RemoveAll(staged); err != nil {
		return fmt.Errorf("native history %s: %w", staged, err)
	}
	if err := copyTree(target, staged); err != nil {
		return fmt.Errorf("native history: copy %s into %s: %w", target, staged, err)
	}
	if err := unlinkIf(linked, link); err != nil {
		return err
	}
	if err := safefs.Rename(historyFs, staged, link); err != nil {
		return fmt.Errorf("native history %s: %w", link, err)
	}
	return nil
}

// unlinkIf removes the history link at link when there is one.
func unlinkIf(linked bool, link string) error {
	if !linked {
		return nil
	}
	if err := hostOS.UnlinkDir(link); err != nil {
		return fmt.Errorf("native history link %s: %w", link, err)
	}
	return nil
}

// copyTree copies src's directories and regular files into dst, owner-only.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, owneronly.DirMode)
		case !d.Type().IsRegular():
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return safefs.WriteFile(historyFs, to, data, owneronly.FileMode, safefs.AllowEmpty())
	})
}
