package iox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteFileInPlace_RefusesSymlinkedDestination is the point of the
// symlink refusal: an in-place write opens its destination BY PATH, so a
// symlink standing where the destination should be redirects the write to
// whatever it points at — a repo-tracked `.claude/.credentials.json` aimed at
// the user's real one turns a seeding write into an arbitrary-file overwrite.
// Both modes are driven, because both open the same way, and the assertion is
// not merely that an error came back but that the LINK TARGET IS UNTOUCHED:
// an error with the bytes already written through would be the bug.
func TestWriteFileInPlace_RefusesSymlinkedDestination(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode InPlaceMode
	}{
		{"truncate", TruncateInPlace},
		{"append", AppendInPlace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			victim := filepath.Join(dir, "real-credentials.json")
			const sentinel = "{\"token\":\"the user's own\"}\n"
			if err := os.WriteFile(victim, []byte(sentinel), 0o600); err != nil {
				t.Fatalf("seed victim: %v", err)
			}
			dst := filepath.Join(dir, "seeded.json")
			if err := os.Symlink(victim, dst); err != nil {
				t.Fatalf("symlink: %v", err)
			}

			err := WriteFileInPlace(dst, tc.mode, []byte("attacker bytes"), 0o600)
			if err == nil {
				t.Fatal("writing to a symlinked destination must be refused")
			}
			if !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("error must say the destination is a symlink, got %v", err)
			}

			got, readErr := os.ReadFile(victim)
			if readErr != nil {
				t.Fatalf("read victim: %v", readErr)
			}
			if string(got) != sentinel {
				t.Fatalf("the symlink's target was written through: got %q, want %q", got, sentinel)
			}
		})
	}
}

// TestWriteFileInPlace_RefusesDanglingSymlinkDestination covers the other
// half of the same attack: a symlink pointing at a path that does not exist
// YET. A create-if-missing open follows it and CREATES the attacker's chosen
// file, which an existence check on the destination would never notice
// because the destination itself "is not there".
func TestWriteFileInPlace_RefusesDanglingSymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "not-yet-there.json")
	dst := filepath.Join(dir, "seeded.json")
	if err := os.Symlink(target, dst); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := WriteFileInPlace(dst, TruncateInPlace, []byte("attacker bytes"), 0o600)
	if err == nil {
		t.Fatal("writing to a dangling symlinked destination must be refused")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error must say the destination is a symlink, got %v", err)
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		t.Fatal("the symlink's target was created: the write followed the link")
	}
}

// TestWriteFileInPlace_TruncateReplacesWholeContent pins that a shorter
// second write does not leave the tail of the first behind — the failure
// mode a bare O_WRONLY without O_TRUNC produces.
func TestWriteFileInPlace_TruncateReplacesWholeContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "creds.json")

	if err := WriteFileInPlace(p, TruncateInPlace, []byte("a much longer first version"), 0o600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFileInPlace(p, TruncateInPlace, []byte("short"), 0o600); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "short" {
		t.Fatalf("got %q, want %q", got, "short")
	}
}

// TestWriteFileInPlace_AppendPreservesPriorContent pins the mode the
// gitignore block append depends on: what was already in the file survives.
func TestWriteFileInPlace_AppendPreservesPriorContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(p, []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := WriteFileInPlace(p, AppendInPlace, []byte("\n# ctxloom\n.ctxloom/cache/\n"), 0o644); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := "*.log\n\n# ctxloom\n.ctxloom/cache/\n"; string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestWriteFileInPlace_AppendCreatesMissingFile pins that append is
// create-if-absent: EnsureFile's first run on a project with no .gitignore at
// all goes down this path.
func TestWriteFileInPlace_AppendCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".gitignore")

	if err := WriteFileInPlace(p, AppendInPlace, []byte("# ctxloom\n"), 0o644); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "# ctxloom\n" {
		t.Fatalf("got %q, want %q", got, "# ctxloom\n")
	}
}

// TestWriteFileInPlace_RefusesZeroValueMode pins the guard that keeps
// in-place from being something a caller falls into: the zero InPlaceMode is
// an error naming the atomic default, not a silent truncate.
func TestWriteFileInPlace_RefusesZeroValueMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var unset InPlaceMode
	err := WriteFileInPlace(p, unset, []byte("new"), 0o644)
	if err == nil {
		t.Fatal("the zero InPlaceMode must be refused")
	}
	if !strings.Contains(err.Error(), "WriteFileAtomic") {
		t.Fatalf("the refusal must point at the default, got %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "original" {
		t.Fatalf("a refused call must not have written: got %q", got)
	}
}

// TestWriteFileInPlace_RefusesEmptyOverExisting mirrors the atomic family's
// guard: zero bytes over a live file is ctxloom's characteristic silent
// no-op-shaped data loss, so it is refused unless the caller says AllowEmpty.
func TestWriteFileInPlace_RefusesEmptyOverExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("live content"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := WriteFileInPlace(p, TruncateInPlace, nil, 0o644); err == nil {
		t.Fatal("zero bytes over an existing file must be refused")
	}
	if got, _ := os.ReadFile(p); string(got) != "live content" {
		t.Fatalf("the refused call truncated anyway: got %q", got)
	}

	if err := WriteFileInPlace(p, TruncateInPlace, nil, 0o644, AllowEmpty()); err != nil {
		t.Fatalf("AllowEmpty must opt out of the guard: %v", err)
	}
	if got, _ := os.ReadFile(p); len(got) != 0 {
		t.Fatalf("AllowEmpty write left %q behind", got)
	}
}

// TestWriteFileInPlace_DurableSyncsParentDirectory pins that Durable() is not
// silently dropped on this entry point: a caller that asked for the directory
// entry to be durable gets the same fsync the atomic family performs, which
// is otherwise unobservable (a durable write produces byte-identical output).
func TestWriteFileInPlace_DurableSyncsParentDirectory(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")

	var synced []string
	restore := SetSyncDirForTesting(func(d string) error {
		synced = append(synced, d)
		return nil
	})
	defer restore()

	if err := WriteFileInPlace(p, TruncateInPlace, []byte("x"), 0o644, Durable()); err != nil {
		t.Fatalf("durable write: %v", err)
	}
	if len(synced) != 1 || synced[0] != dir {
		t.Fatalf("Durable() must fsync the parent directory once, got %v", synced)
	}

	synced = nil
	if err := WriteFileInPlace(p, TruncateInPlace, []byte("y"), 0o644); err != nil {
		t.Fatalf("plain write: %v", err)
	}
	if len(synced) != 0 {
		t.Fatalf("a write without Durable() must not fsync the directory, got %v", synced)
	}
}

// TestCloseChecked_PropagatesCloseError pins that a discarded Close would
// hide a write-never-reached-disk failure (ENOSPC/EDQUOT/EIO) behind a nil
// error and report success. The worst case is an APPEND caller that has
// already removed content elsewhere on the strength of that success — the
// gitignore migration path, which removes the superseded blanket rule before
// the replacement block is appended, so a silently-failed Close leaves the
// project with FEWER ignore rules than it started with. (This test moved here
// with that append: it pinned internal/gitignore's own closeChecked before
// appendBlock delegated to this package.) Forcing a REAL ENOSPC is
// impractical in a portable unit test, so this drives the exact propagation
// path via an already-closed *os.File, whose second Close reliably errors.
func TestCloseChecked_PropagatesCloseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("first close must succeed: %v", err)
	}

	err = closeChecked(f, path)
	if err == nil {
		t.Fatal("a second Close on an already-closed file must error, and closeChecked must surface it rather than discard it")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("the error must name the file, not just the bare OS error: %v", err)
	}
}
