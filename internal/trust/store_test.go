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
// tests exercise a mid-mutation save failure without a half-written file.
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

	if err := s.SetAccepted("https://github.com/acme/repo", "tooling#mcp/postgres", "sha256:raw", ""); err != nil {
		t.Fatalf("SetAccepted: %v", err)
	}
	if err := s.SetAccepted("https://github.com/acme/repo", "lib#fragments/solid", "sha256:r2", "sha256:d2"); err != nil {
		t.Fatalf("SetAccepted(pair): %v", err)
	}
	if err := s.SetRejected("https://github.com/acme/repo", "tooling#fragments/curl-pipe-sh", "sha256:c0ffee"); err != nil {
		t.Fatalf("SetRejected: %v", err)
	}

	// Reload from the same fs into a fresh store: a genuine save→reload, not a
	// shared pointer.
	reloaded, err := New(".ctxloom/trust.yaml", WithFS(fs))
	if err != nil {
		t.Fatalf("reload New: %v", err)
	}
	item, ok := reloaded.Lookup("https://github.com/acme/repo", "tooling#mcp/postgres")
	if !ok || item.State != StateAccepted || item.RawHash != "sha256:raw" || item.DistilledHash != "" {
		t.Errorf("accepted single-slot item did not survive round-trip: %+v (found=%v)", item, ok)
	}
	pair, ok := reloaded.Lookup("https://github.com/acme/repo", "lib#fragments/solid")
	if !ok || pair.State != StateAccepted || pair.RawHash != "sha256:r2" || pair.DistilledHash != "sha256:d2" {
		t.Errorf("accepted hash-pair item did not survive round-trip: %+v (found=%v)", pair, ok)
	}
	rej, ok := reloaded.Lookup("https://github.com/acme/repo", "tooling#fragments/curl-pipe-sh")
	if !ok || rej.State != StateRejected {
		t.Errorf("rejected item did not survive round-trip: %+v (found=%v)", rej, ok)
	}
	if !reloaded.DeniedHash("sha256:c0ffee") {
		t.Error("denylist did not survive round-trip")
	}

	// The persisted file is version 2 with the state vocabulary.
	raw, _ := afero.ReadFile(fs, ".ctxloom/trust.yaml")
	if !strings.Contains(string(raw), "version: 2") ||
		!strings.Contains(string(raw), "state: accepted") ||
		!strings.Contains(string(raw), "state: rejected") {
		t.Errorf("trust.yaml v2 vocabulary wrong:\n%s", raw)
	}
}

func TestStore_SetRejectedWritesBothComponents(t *testing.T) {
	s := newMemStore(t)
	if err := s.SetRejected("https://github.com/acme/repo", "b#fragments/x", "sha256:beef", "sha256:dead"); err != nil {
		t.Fatalf("SetRejected: %v", err)
	}
	if item, ok := s.Lookup("https://github.com/acme/repo", "b#fragments/x"); !ok || item.State != StateRejected {
		t.Error("ref-level rejected state not written")
	}
	if !s.DeniedHash("sha256:beef") || !s.DeniedHash("sha256:dead") {
		t.Error("content-hash denylist companions not written for every supplied hash")
	}
}

func TestStore_SetRejectedNoHashesSkipsDenylist(t *testing.T) {
	s := newMemStore(t)
	if err := s.SetRejected("https://github.com/acme/repo", "b#fragments/x", ""); err != nil {
		t.Fatalf("SetRejected: %v", err)
	}
	if item, ok := s.Lookup("https://github.com/acme/repo", "b#fragments/x"); !ok || item.State != StateRejected {
		t.Error("ref-level rejection must be written even with no resolvable hash")
	}
	if s.DeniedHash("") {
		t.Error("empty hash must not be added to denylist")
	}
}

func TestStore_SetAcceptedRequiresAHash(t *testing.T) {
	s := newMemStore(t)
	if err := s.SetAccepted("https://github.com/acme/repo", "b#fragments/x", "", ""); err == nil {
		t.Fatal("SetAccepted with no hashes must error — an acceptance must pin content")
	}
	if _, ok := s.Lookup("https://github.com/acme/repo", "b#fragments/x"); ok {
		t.Error("a failed acceptance must not leave an item state behind")
	}
}

func TestStore_SetAcceptedOverwritesRejected(t *testing.T) {
	s := newMemStore(t)
	if err := s.SetRejected("https://github.com/acme/repo", "b#fragments/x", "sha256:old"); err != nil {
		t.Fatalf("SetRejected: %v", err)
	}
	if err := s.SetAccepted("https://github.com/acme/repo", "b#fragments/x", "sha256:new", ""); err != nil {
		t.Fatalf("SetAccepted: %v", err)
	}
	item, ok := s.Lookup("https://github.com/acme/repo", "b#fragments/x")
	if !ok || item.State != StateAccepted || item.RawHash != "sha256:new" {
		t.Errorf("re-accept must overwrite the rejected state: %+v", item)
	}
	// The old content's denylist entry survives — only the ref state flipped.
	if !s.DeniedHash("sha256:old") {
		t.Error("re-accepting a ref must not scrub the rejected content's denylist entry")
	}
}

func TestStore_Remove(t *testing.T) {
	s := newMemStore(t)
	if err := s.SetAccepted("https://github.com/acme/repo", "b#fragments/x", "sha256:h", ""); err != nil {
		t.Fatalf("SetAccepted: %v", err)
	}
	if err := s.Remove("https://github.com/acme/repo", "b#fragments/x"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := s.Lookup("https://github.com/acme/repo", "b#fragments/x"); ok {
		t.Error("Remove must drop the item's state (back to implicit pending)")
	}
	// Removing an absent item is a no-op.
	if err := s.Remove("https://github.com/acme/repo", "b#fragments/never"); err != nil {
		t.Errorf("Remove(absent) = %v, want nil", err)
	}
}

func TestStore_RollbackOnSaveFailure(t *testing.T) {
	ffs := &failableFs{Fs: afero.NewMemMapFs()}
	s, err := New("", WithFS(ffs))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A first, successful acceptance.
	if err := s.SetAccepted("https://github.com/acme/repo", "b#fragments/keep", "sha256:keep", ""); err != nil {
		t.Fatalf("SetAccepted(keep): %v", err)
	}

	// Arm the failure and attempt a second acceptance; save() must fail and the
	// in-memory store must be unchanged.
	ffs.fail = true
	err = s.SetAccepted("https://github.com/acme/repo", "b#fragments/drop", "sha256:drop", "")
	if err == nil {
		t.Fatal("expected SetAccepted to fail with a failing fs")
	}
	if _, ok := s.Lookup("https://github.com/acme/repo", "b#fragments/drop"); ok {
		t.Error("failed acceptance must not remain in the in-memory store (rollback)")
	}
	if item, ok := s.Lookup("https://github.com/acme/repo", "b#fragments/keep"); !ok || item.RawHash != "sha256:keep" {
		t.Error("the prior successful acceptance must be intact after a rollback")
	}

	// A failed overwrite must restore the PREVIOUS state, not drop it.
	err = s.SetAccepted("https://github.com/acme/repo", "b#fragments/keep", "sha256:changed", "")
	if err == nil {
		t.Fatal("expected overwriting SetAccepted to fail with a failing fs")
	}
	if item, ok := s.Lookup("https://github.com/acme/repo", "b#fragments/keep"); !ok || item.RawHash != "sha256:keep" {
		t.Errorf("failed overwrite must restore the previous item, got %+v (found=%v)", item, ok)
	}

	// SetRejected must roll back BOTH components on failure.
	err = s.SetRejected("https://github.com/acme/repo", "b#fragments/bad", "sha256:bad")
	if err == nil {
		t.Fatal("expected SetRejected to fail with a failing fs")
	}
	if _, ok := s.Lookup("https://github.com/acme/repo", "b#fragments/bad"); ok {
		t.Error("failed rejection must roll back the ref-level state")
	}
	if s.DeniedHash("sha256:bad") {
		t.Error("failed rejection must roll back the denylist component")
	}

	// Remove rolls back on save failure too.
	err = s.Remove("https://github.com/acme/repo", "b#fragments/keep")
	if err == nil {
		t.Fatal("expected Remove to fail with a failing fs")
	}
	if _, ok := s.Lookup("https://github.com/acme/repo", "b#fragments/keep"); !ok {
		t.Error("failed Remove must restore the item (rollback)")
	}
}

func TestStore_MissingFileIsEmpty(t *testing.T) {
	s := newMemStore(t)
	if s.DeniedHash("sha256:anything") {
		t.Error("empty store should match nothing")
	}
	if _, ok := s.Lookup("https://github.com/acme/repo", "whatever#fragments/x"); ok {
		t.Error("empty store should have no item states")
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

func TestStore_UnknownItemStateErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	v2 := `version: 2
items:
  - repo_url: https://github.com/acme/repo
    ref: b#fragments/x
    state: blessed
`
	if err := afero.WriteFile(fs, ".ctxloom/trust.yaml", []byte(v2), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := New(".ctxloom/trust.yaml", WithFS(fs)); err == nil {
		t.Error("an unknown item state must fail the load (fail closed), not be guessed")
	}
}

// --- v1 → v2 migration --------------------------------------------------------

// v1StoreYAML is a populated version-1 trust.yaml: grants in both forms
// (including two grants for one ref and a repo-URL spelling variant), a
// blacklist entry, a blacklist+grant conflict, a denylist, bundle postures,
// and the baseline marker.
const v1StoreYAML = `version: 1
baseline_version: 1
baselined_at: "2026-01-02T03:04:05Z"
grants:
  - repo_url: https://github.com/Acme/Repo.git
    ref: tooling#fragments/solid
    content_hash: sha256:rawhash
    form: raw
    sha_at_grant: 9f3c1d7
  - repo_url: https://github.com/acme/repo
    ref: lib#fragments/guide
    content_hash: sha256:distilledhash
    form: distilled
  - repo_url: https://github.com/acme/repo
    ref: lib#fragments/guide
    content_hash: sha256:rawtwin
    form: raw
  - repo_url: https://github.com/acme/repo
    ref: banned#fragments/bad
    content_hash: sha256:grantedbutbanned
    form: raw
blacklist:
  - repo_url: https://github.com/acme/repo
    ref: tooling#fragments/evil
  - repo_url: https://github.com/acme/repo
    ref: banned#fragments/bad
denylist:
  - sha256:deadbeef
bundles:
  - bundle: blessed
    decision: trusted
  - bundle: experimental
    decision: untrusted
`

func loadV1Migrated(t *testing.T) (*Store, afero.Fs) {
	t.Helper()
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, ".ctxloom/trust.yaml", []byte(v1StoreYAML), 0o644); err != nil {
		t.Fatalf("seed v1 store: %v", err)
	}
	s, err := New(".ctxloom/trust.yaml", WithFS(fs))
	if err != nil {
		t.Fatalf("New(v1 store): %v", err)
	}
	return s, fs
}

func TestStore_MigrateV1_GrantsFillFormSlots(t *testing.T) {
	s, _ := loadV1Migrated(t)

	// A raw-form grant fills raw_hash only (lazy pair), keyed canonically even
	// when the v1 record spelled the repo URL differently.
	item, ok := s.Lookup("https://github.com/acme/repo", "tooling#fragments/solid")
	if !ok || item.State != StateAccepted {
		t.Fatalf("raw grant did not migrate to accepted: %+v (found=%v)", item, ok)
	}
	if item.RawHash != "sha256:rawhash" || item.DistilledHash != "" {
		t.Errorf("raw grant slots = {raw:%q distilled:%q}, want {sha256:rawhash, empty}", item.RawHash, item.DistilledHash)
	}

	// Two grants (one per form) for the same ref merge into one item with both
	// slots filled.
	pair, ok := s.Lookup("https://github.com/acme/repo", "lib#fragments/guide")
	if !ok || pair.State != StateAccepted {
		t.Fatalf("dual-form grants did not migrate: %+v (found=%v)", pair, ok)
	}
	if pair.RawHash != "sha256:rawtwin" || pair.DistilledHash != "sha256:distilledhash" {
		t.Errorf("dual-form slots = {raw:%q distilled:%q}, want {sha256:rawtwin, sha256:distilledhash}", pair.RawHash, pair.DistilledHash)
	}
}

func TestStore_MigrateV1_BlacklistBecomesRejected(t *testing.T) {
	s, _ := loadV1Migrated(t)

	if item, ok := s.Lookup("https://github.com/acme/repo", "tooling#fragments/evil"); !ok || item.State != StateRejected {
		t.Errorf("blacklist entry did not migrate to rejected: %+v (found=%v)", item, ok)
	}
	// Rejected beats a coexisting v1 grant for the same ref.
	if item, ok := s.Lookup("https://github.com/acme/repo", "banned#fragments/bad"); !ok || item.State != StateRejected {
		t.Errorf("blacklist+grant conflict must migrate rejected: %+v (found=%v)", item, ok)
	}
	if !s.DeniedHash("sha256:deadbeef") {
		t.Error("v1 denylist entry must carry over")
	}
}

func TestStore_MigrateV1_PureReadDoesNotRewrite(t *testing.T) {
	_, fs := loadV1Migrated(t)
	raw, err := afero.ReadFile(fs, ".ctxloom/trust.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(raw) != v1StoreYAML {
		t.Error("a pure read of a v1 store must not rewrite the file")
	}
}

func TestStore_MigrateV1_FirstMutationPersistsV2(t *testing.T) {
	s, fs := loadV1Migrated(t)
	if err := s.SetAccepted("https://github.com/acme/repo", "new#fragments/n", "sha256:n", ""); err != nil {
		t.Fatalf("SetAccepted: %v", err)
	}
	raw, _ := afero.ReadFile(fs, ".ctxloom/trust.yaml")
	text := string(raw)
	if !strings.Contains(text, "version: 2") {
		t.Errorf("first mutation must persist the migrated v2 form:\n%s", text)
	}
	for _, legacy := range []string{"grants:", "blacklist:", "bundles:", "baseline_version"} {
		if strings.Contains(text, legacy) {
			t.Errorf("persisted v2 store must not carry legacy section %q:\n%s", legacy, text)
		}
	}

	// The migrated states survive a reload of the persisted v2 file.
	reloaded, err := New(".ctxloom/trust.yaml", WithFS(fs))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if item, ok := reloaded.Lookup("https://github.com/acme/repo", "tooling#fragments/solid"); !ok || item.RawHash != "sha256:rawhash" {
		t.Errorf("migrated grant lost across v2 persist/reload: %+v (found=%v)", item, ok)
	}
	if item, ok := reloaded.Lookup("https://github.com/acme/repo", "banned#fragments/bad"); !ok || item.State != StateRejected {
		t.Errorf("migrated rejection lost across v2 persist/reload: %+v (found=%v)", item, ok)
	}
	if !reloaded.DeniedHash("sha256:deadbeef") {
		t.Error("denylist lost across v2 persist/reload")
	}
}
