package scm

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// mustSubmodulePaths is the existing tests' reader: they exercise the
// happy/absent paths, where an error is itself a failure.
func mustSubmodulePaths(t *testing.T, fsys afero.Fs, dir string) []string {
	t.Helper()
	paths, err := SubmodulePaths(fsys, dir)
	if err != nil {
		t.Fatalf("SubmodulePaths(%q): %v", dir, err)
	}
	return paths
}

// unreadableFs makes exactly one path fail to open with EACCES; everything else
// behaves like the wrapped fs. It models the case MemMapFs alone cannot: a
// .gitmodules that EXISTS but cannot be read.
type unreadableFs struct {
	afero.Fs
	deny string
}

func (u unreadableFs) Open(name string) (afero.File, error) {
	if strings.HasSuffix(name, u.deny) {
		return nil, &os.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return u.Fs.Open(name)
}

func (u unreadableFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if strings.HasSuffix(name, u.deny) {
		return nil, &os.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return u.Fs.OpenFile(name, flag, perm)
}

// TestSubmodulePaths_UnreadableIsNotAbsent pins U073-F06's root cause: an
// UNREADABLE .gitmodules used to be indistinguishable from an ABSENT one — both
// yielded nil, which ExpandSubmodules turns into a rule with zero patterns that
// silently guards nothing. "There are no submodules" and "I could not find out
// whether there are submodules" are different answers and the guard must be
// able to tell them apart.
func TestSubmodulePaths_UnreadableIsNotAbsent(t *testing.T) {
	base := afero.NewMemMapFs()
	if err := afero.WriteFile(base, "/repo/.gitmodules", []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(base, "/repo/.git", []byte("gitdir: x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsys := unreadableFs{Fs: base, deny: ".gitmodules"}

	paths, err := SubmodulePaths(fsys, "/repo")
	if err == nil {
		t.Fatal("an unreadable .gitmodules must be an error, not an empty submodule set")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("the underlying cause must survive: got %v", err)
	}
	if paths != nil {
		t.Errorf("no paths may be reported alongside the failure, got %v", paths)
	}
}

// The absent case stays quiet: a repo root with no .gitmodules legitimately has
// no submodules. This is the discriminator that keeps the fix above from
// turning every submodule-free repo into an error.
func TestSubmodulePaths_AbsentIsQuiet(t *testing.T) {
	fsys := afero.NewMemMapFs()
	if err := afero.WriteFile(fsys, "/repo/.git/HEAD", []byte("ref: x"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := SubmodulePaths(fsys, "/repo")
	if err != nil {
		t.Fatalf("a repo with no .gitmodules is not an error: %v", err)
	}
	if paths != nil {
		t.Errorf("want nil paths, got %v", paths)
	}
}
