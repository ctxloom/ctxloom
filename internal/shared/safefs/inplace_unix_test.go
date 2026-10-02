//go:build !windows

package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/spf13/afero"
)

// TestWriteFileInPlace_SymlinkRefusalComesFromTheOpenSyscall is the proof
// that the refusal is not a stat-then-open check. A stat-based check can only
// ever report what the path looked like a moment ago, so an attacker who
// plants the symlink between the check and the open wins; O_NOFOLLOW makes
// the kernel refuse at the same instant it resolves the path, leaving no
// window at all. What distinguishes the two from outside is the ERRNO: only
// the kernel refusing O_NOFOLLOW on a symlink produces ELOOP. If this
// assertion ever fails while the plain refusal test still passes, the
// implementation has quietly regressed to a check.
func TestWriteFileInPlace_SymlinkRefusalComesFromTheOpenSyscall(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("mine"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dst := filepath.Join(dir, "dst")
	if err := os.Symlink(victim, dst); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := WriteFileInPlace(dst, TruncateInPlace, []byte("theirs"), 0o600)
	if err == nil {
		t.Fatal("a symlinked destination must be refused")
	}
	if !errors.Is(err, syscall.ELOOP) && !errors.Is(err, syscall.EMLINK) {
		t.Fatalf("the refusal must carry the kernel's O_NOFOLLOW errno (ELOOP/EMLINK), got %v — a stat-based check would not", err)
	}
}

// TestWriteFileInPlace_KeepsTheSameInode is the property the whole primitive
// exists for, asserted differentially against the atomic default rather than
// described. A rename-based write installs a NEW inode at the path, which is
// what fails with EBUSY over a bind mount and what swaps the file out from
// under an flock or an open reader. An in-place write must land on the inode
// that was already there.
func TestWriteFileInPlace_KeepsTheSameInode(t *testing.T) {
	dir := t.TempDir()

	inode := func(t *testing.T, path string) uint64 {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			t.Skip("no syscall.Stat_t on this platform")
		}
		return st.Ino
	}

	inPlace := filepath.Join(dir, "inplace")
	if err := os.WriteFile(inPlace, []byte("v1"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := inode(t, inPlace)
	if err := WriteFileInPlace(inPlace, TruncateInPlace, []byte("v2"), 0o644); err != nil {
		t.Fatalf("in-place write: %v", err)
	}
	if after := inode(t, inPlace); after != before {
		t.Fatalf("in-place write replaced the inode (%d -> %d); it must write through the existing one", before, after)
	}

	// The control: the same sequence through the atomic default, which MUST
	// replace the inode. Without it, an implementation that had silently
	// become rename-based could still pass the assertion above on a
	// filesystem that happened to reuse the number.
	atomic := filepath.Join(dir, "atomic")
	if err := os.WriteFile(atomic, []byte("v1"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	atomicBefore := inode(t, atomic)
	if err := WriteFile(afero.NewOsFs(), atomic, []byte("v2"), 0o644); err != nil {
		t.Fatalf("atomic write: %v", err)
	}
	if atomicAfter := inode(t, atomic); atomicAfter == atomicBefore {
		t.Fatal("fixture is not discriminating: the atomic write kept the inode, so the in-place assertion proves nothing")
	}
}

// TestWriteFileInPlace_PermAppliesOnCreateOnly pins both halves of the perm
// contract under a hostile umask: exact (umask-free) on a file this call
// created, matching safefs.WriteFile's documented divergence from
// os.WriteFile; and UNTOUCHED on a file that already existed, because an
// in-place write goes into a file that already belongs to someone and
// widening a 0600 file to the caller's 0644 default would be a side effect
// nobody asked for.
func TestWriteFileInPlace_PermAppliesOnCreateOnly(t *testing.T) {
	const restrictive = 0o077
	prev := syscall.Umask(restrictive)
	defer syscall.Umask(prev)

	dir := t.TempDir()
	requireHostileUmask(t, dir)

	created := filepath.Join(dir, "created")
	if err := WriteFileInPlace(created, TruncateInPlace, []byte("x"), 0o644); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := permOf(t, created); got != 0o644 {
		t.Fatalf("perm on create: got %#o, want %#o exactly; the umask must not narrow it", got, 0o644)
	}

	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(existing, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := WriteFileInPlace(existing, TruncateInPlace, []byte("new"), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	if got := permOf(t, existing); got != 0o600 {
		t.Fatalf("perm on an existing file: got %#o, want it left at %#o", got, 0o600)
	}
}

// requireHostileUmask is the fixture check: with umask 077 in force the
// stdlib writer must produce 0600 from a 0644 request, or the umask never
// reached the code under test and the exactness claim would be vacuous.
func requireHostileUmask(t *testing.T, dir string) {
	t.Helper()
	stdlib := filepath.Join(dir, "stdlib")
	if err := os.WriteFile(stdlib, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := permOf(t, stdlib); got != 0o600 {
		t.Fatalf("fixture is not hostile: the umask did not mask os.WriteFile's mode (got %#o)", got)
	}
}

// permOf is path's permission bits.
func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return fi.Mode().Perm()
}
