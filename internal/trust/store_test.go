package trust

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// failableFs wraps an afero.Fs and fails directory creation once armed, so a
// save() (which MkdirAll's before writing) fails on demand. Lets the rollback
// test exercise a mid-mutation save failure without a half-written file.
type failableFs struct {
	afero.Fs
	fail bool
}

func (f *failableFs) MkdirAll(path string, perm os.FileMode) error {
	if f.fail {
		return errors.New("injected mkdir failure")
	}
	return f.Fs.MkdirAll(path, perm)
}

func newMemStore(t *testing.T) *Store {
	t.Helper()
	s, err := New("", WithFS(afero.NewMemMapFs()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestStore_RoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	s, err := New(".ctxloom/trust.yaml", WithFS(fs))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := s.AddGrant("https://github.com/acme/repo", "tooling#mcp/postgres", "sha256:dead", "raw", "1a2b3c4"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if err := s.Blacklist("https://github.com/acme/repo", "tooling#fragments/curl-pipe-sh", "sha256:c0ffee"); err != nil {
		t.Fatalf("Blacklist: %v", err)
	}
	if err := s.SetBundle("experimental", Deny); err != nil {
		t.Fatalf("SetBundle: %v", err)
	}
	if err := s.SetBundle("code-quality", Allow); err != nil {
		t.Fatalf("SetBundle: %v", err)
	}

	// Reload from the same fs into a fresh store: a genuine save→reload, not a
	// shared pointer.
	reloaded, err := New(".ctxloom/trust.yaml", WithFS(fs))
	if err != nil {
		t.Fatalf("reload New: %v", err)
	}
	if _, ok := reloaded.GrantMatch("https://github.com/acme/repo", "tooling#mcp/postgres", "sha256:dead"); !ok {
		t.Error("grant did not survive round-trip")
	}
	if !reloaded.BlacklistMatch("https://github.com/acme/repo", "tooling#fragments/curl-pipe-sh") {
		t.Error("blacklist did not survive round-trip")
	}
	if !reloaded.DenylistMatch("sha256:c0ffee") {
		t.Error("denylist did not survive round-trip")
	}
	if dec, ok := reloaded.BundlePosture("experimental"); !ok || dec != Deny {
		t.Errorf("bundle posture experimental = %v, %v; want deny, true", dec, ok)
	}
	if dec, ok := reloaded.BundlePosture("code-quality"); !ok || dec != Allow {
		t.Errorf("bundle posture code-quality = %v, %v; want allow, true", dec, ok)
	}

	// The on-disk vocabulary for bundle posture is trusted/untrusted (plan), not
	// allow/deny.
	raw, _ := afero.ReadFile(fs, ".ctxloom/trust.yaml")
	if !strings.Contains(string(raw), "decision: trusted") || !strings.Contains(string(raw), "decision: untrusted") {
		t.Errorf("trust.yaml bundle posture vocabulary wrong:\n%s", raw)
	}
}

func TestStore_BlacklistWritesBothComponents(t *testing.T) {
	s := newMemStore(t)
	if err := s.Blacklist("https://github.com/acme/repo", "b#fragments/x", "sha256:beef"); err != nil {
		t.Fatalf("Blacklist: %v", err)
	}
	if !s.BlacklistMatch("https://github.com/acme/repo", "b#fragments/x") {
		t.Error("ref-level blacklist not written")
	}
	if !s.DenylistMatch("sha256:beef") {
		t.Error("content-hash denylist companion not written")
	}
}

func TestStore_BlacklistEmptyHashSkipsDenylist(t *testing.T) {
	s := newMemStore(t)
	if err := s.Blacklist("https://github.com/acme/repo", "b#fragments/x", ""); err != nil {
		t.Fatalf("Blacklist: %v", err)
	}
	if !s.BlacklistMatch("https://github.com/acme/repo", "b#fragments/x") {
		t.Error("ref-level blacklist must be written even with empty hash")
	}
	if s.DenylistMatch("") {
		t.Error("empty hash must not be added to denylist")
	}
}

func TestStore_RollbackOnSaveFailure(t *testing.T) {
	ffs := &failableFs{Fs: afero.NewMemMapFs()}
	s, err := New("", WithFS(ffs))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A first, successful grant.
	if err := s.AddGrant("https://github.com/acme/repo", "b#fragments/keep", "sha256:keep", "raw", ""); err != nil {
		t.Fatalf("AddGrant(keep): %v", err)
	}

	// Arm the failure and attempt a second grant; save() must fail and the
	// in-memory store must be unchanged.
	ffs.fail = true
	err = s.AddGrant("https://github.com/acme/repo", "b#fragments/drop", "sha256:drop", "raw", "")
	if err == nil {
		t.Fatal("expected AddGrant to fail with a failing fs")
	}
	if _, ok := s.GrantMatch("https://github.com/acme/repo", "b#fragments/drop", "sha256:drop"); ok {
		t.Error("failed grant must not remain in the in-memory store (rollback)")
	}
	if _, ok := s.GrantMatch("https://github.com/acme/repo", "b#fragments/keep", "sha256:keep"); !ok {
		t.Error("the prior successful grant must be intact after a rollback")
	}

	// Blacklist must roll back BOTH components on failure.
	err = s.Blacklist("https://github.com/acme/repo", "b#fragments/bad", "sha256:bad")
	if err == nil {
		t.Fatal("expected Blacklist to fail with a failing fs")
	}
	if s.BlacklistMatch("https://github.com/acme/repo", "b#fragments/bad") || s.DenylistMatch("sha256:bad") {
		t.Error("failed blacklist must roll back both ref-level and denylist components")
	}
}

func TestStore_MissingFileIsEmpty(t *testing.T) {
	s := newMemStore(t)
	if s.DenylistMatch("sha256:anything") {
		t.Error("empty store should match nothing")
	}
	if _, ok := s.BundlePosture("whatever"); ok {
		t.Error("empty store should have no bundle posture")
	}
}

func TestStore_CorruptFileErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, ".ctxloom/trust.yaml", []byte("this: : : not valid yaml: ["), 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	if _, err := New(".ctxloom/trust.yaml", WithFS(fs)); err == nil {
		t.Error("New must return an error on a corrupt trust store so callers can fail closed")
	}
}
