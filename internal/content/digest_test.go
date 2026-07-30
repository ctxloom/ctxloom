package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDigest_ShapeIsSHA256SUMSWithVersionMarker(t *testing.T) {
	got, err := Digest([]Component{
		{Path: "fragments/solid.md", Bytes: []byte("hello\n")},
		{Path: "fragments/.solid.meta.yaml", Bytes: []byte("tags: [a]\n")},
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if lines[0] != DigestVersionMarker {
		t.Fatalf("first line = %q, want the version marker %q", lines[0], DigestVersionMarker)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want marker + 2 components: %q", len(lines), got)
	}
	shape := regexp.MustCompile(`^[0-9a-f]{64}  \S`)
	for _, line := range lines[1:] {
		if !shape.MatchString(line) {
			t.Errorf("line %q is not <64 hex><2 spaces><path>", line)
		}
	}
	// The hash must be a plain sha256 of the bytes — nothing salted or framed.
	sum := sha256.Sum256([]byte("hello\n"))
	want := hex.EncodeToString(sum[:]) + "  fragments/solid.md"
	if !strings.Contains(string(got), want) {
		t.Errorf("digest does not contain %q:\n%s", want, got)
	}
}

func TestDigest_SortsByPathAndIsIndependentOfInputOrder(t *testing.T) {
	a := []Component{
		{Path: "b.md", Bytes: []byte("b")},
		{Path: "a.md", Bytes: []byte("a")},
		{Path: "c/d.md", Bytes: []byte("d")},
	}
	b := []Component{a[2], a[0], a[1]}
	da, err := Digest(a)
	if err != nil {
		t.Fatalf("Digest(a): %v", err)
	}
	db, err := Digest(b)
	if err != nil {
		t.Fatalf("Digest(b): %v", err)
	}
	if !bytes.Equal(da, db) {
		t.Fatalf("input order changed the digest:\n%s\n---\n%s", da, db)
	}
	paths := regexp.MustCompile(`(?m)^[0-9a-f]{64}  (.*)$`).FindAllStringSubmatch(string(da), -1)
	var got []string
	for _, m := range paths {
		got = append(got, m[1])
	}
	want := []string{"a.md", "b.md", "c/d.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v, want byte-wise sorted %v", got, want)
	}
}

func TestDigest_ModeIsNotInTheDigest(t *testing.T) {
	// Mode is not portable — a Windows checkout yields different mode bits — so
	// a mode-bearing digest would be platform-dependent and the tree would fail
	// its own signature. Executability is declared metadata instead, and that
	// declaration reaches the digest as sidecar BYTES, not as a mode field.
	regular, err := Digest([]Component{{Path: "scripts/run.sh", Mode: ModeRegular, Bytes: []byte("#!/bin/sh\n")}})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	executable, err := Digest([]Component{{Path: "scripts/run.sh", Mode: ModeExecutable, Bytes: []byte("#!/bin/sh\n")}})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if !bytes.Equal(regular, executable) {
		t.Fatalf("ComponentMode leaked into the digest:\n%s\n---\n%s", regular, executable)
	}
}

func TestDigest_ContentChangeChangesDigest(t *testing.T) {
	base, err := Digest([]Component{{Path: "a.md", Bytes: []byte("one")}})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	for name, comp := range map[string]Component{
		"changed bytes": {Path: "a.md", Bytes: []byte("two")},
		"changed path":  {Path: "b.md", Bytes: []byte("one")},
	} {
		got, err := Digest([]Component{comp})
		if err != nil {
			t.Fatalf("Digest(%s): %v", name, err)
		}
		if bytes.Equal(base, got) {
			t.Errorf("%s did not change the digest", name)
		}
	}
}

func TestDigest_RefusesUnencodablePaths(t *testing.T) {
	for name, path := range map[string]string{
		"empty":            "",
		"newline":          "a\nb.md",
		"backslash":        `a\b.md`,
		"absolute":         "/etc/passwd",
		"traversal":        "../outside.md",
		"nested traversal": "a/../../outside.md",
	} {
		if _, err := Digest([]Component{{Path: path, Bytes: []byte("x")}}); !errors.Is(err, ErrBadPath) {
			t.Errorf("%s (%q): err = %v, want ErrBadPath", name, path, err)
		}
	}
}

func TestDigest_RefusesDuplicatePaths(t *testing.T) {
	_, err := Digest([]Component{
		{Path: "a.md", Bytes: []byte("one")},
		{Path: "a.md", Bytes: []byte("two")},
	})
	if !errors.Is(err, ErrBadPath) {
		t.Fatalf("err = %v, want ErrBadPath for a duplicate path", err)
	}
}

// TestDigest_VerifiesWithStockSha256sum is the interoperability claim the format
// was chosen for: the digest is a real SHA256SUMS file, comment line included, so
// a consumer can check a tree with the stock tool and no ctxloom binary.
func TestDigest_VerifiesWithStockSha256sum(t *testing.T) {
	bin, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skip("sha256sum not available")
	}
	dir := t.TempDir()
	files := map[string]string{"a.md": "alpha\n", "sub/b.md": "beta\n"}
	var components []Component
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		components = append(components, Component{Path: name, Bytes: []byte(body)})
	}
	digest, err := Digest(components)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	sumsPath := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(sumsPath, digest, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cmd := exec.Command(bin, "-c", "SHA256SUMS")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sha256sum -c rejected our digest: %v\n%s\n--- digest ---\n%s", err, out, digest)
	}
	// And it must FAIL once a covered file changes.
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cmd = exec.Command(bin, "-c", "SHA256SUMS")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("sha256sum -c accepted a tampered tree:\n%s", out)
	}
}
