package bundles

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// effHash is the effective-content hash of a raw body — the exact key the trust
// gate receives for an undistilled fragment/prompt (preferDistilled true, no
// distilled form ⇒ raw bytes).
func effHash(body string) string {
	h, _ := (&BundleFragment{Content: body}).EffectiveContentHash(true)
	return h
}

// hashGate allows only items whose effective-content hash is in allowed,
// recording every ref it denies. It mirrors the real per-(ref, content_hash)
// cascade: a single version-less ref can have one version trusted (its hash in
// allowed) and another withheld (its hash absent), which is exactly how two
// commit-versions of one ref are gated independently.
func hashGate(allowed map[string]bool) Authorizer {
	return authorizerFunc(func(e Exposure) Verdict {
		if allowed[HashPayload(e.Bytes)] {
			return admitVerdict()
		}
		return denyVerdict()
	})
}

// versionedLoader builds a loader seeded with a default (lockfile-pinned) bundle
// for canonicalRef and a fake version resolver serving the given per-commit
// bundles. A commit absent from versions resolves to an error (simulating a
// per-version fetch failure).
func versionedLoader(t *testing.T, canonicalRef string, def *Bundle, versions map[string]*Bundle, gate Authorizer) *Pipeline {
	t.Helper()
	resolver := func(_canonical, commit string) (*Bundle, error) {
		b, ok := versions[commit]
		if !ok {
			return nil, fmt.Errorf("fake resolver: no commit %q", commit)
		}
		clone := *b // copy so the loader's Name stamp doesn't clobber the fixture
		return &clone, nil
	}
	return NewPipeline(
		NewLoader(seedLocal(map[string]*Bundle{canonicalRef: def})).WithVersionResolver(resolver),
		gate, true)
}

const cqRef = "https://github.com/acme/b@bundles/cq"
const cqFrag = cqRef + "#fragments/solid"

// TestMultiVersion_CoexistGatedIndependently proves two commit-versions of one
// ref coexist in a single resolution, each gated by its OWN content hash: v1 is
// trusted (its hash granted) and v2 is withheld (blacklisted/un-granted), so only
// v1 survives — the headline multi-version contract.
func TestMultiVersion_CoexistGatedIndependently(t *testing.T) {
	def := &Bundle{Fragments: map[string]BundleFragment{"solid": {Content: "default body"}}}
	versions := map[string]*Bundle{
		"c1": {Fragments: map[string]BundleFragment{"solid": {Content: "v1 body"}}},
		"c2": {Fragments: map[string]BundleFragment{"solid": {Content: "v2 body"}}},
	}
	// Trust v1's content only; v2's hash is absent ⇒ withheld.
	gate := hashGate(map[string]bool{effHash("v1 body"): true})
	l := versionedLoader(t, cqRef, def, versions, gate)

	got := l.ResolveFragmentVersions(cqFrag, []string{"c1", "c2"})
	if len(got) != 1 {
		t.Fatalf("got %d versions, want 1 (v2 withheld)", len(got))
	}
	if got[0].Content != "v1 body" {
		t.Errorf("surviving content = %q, want %q", got[0].Content, "v1 body")
	}
	// The withheld v2 is tallied under the version-less ref (content-free).
	if w := l.Withheld(); len(w) != 1 || w[0] != cqFrag {
		t.Errorf("Withheld() = %v, want [%s]", w, cqFrag)
	}
}

// TestMultiVersion_IdenticalContentDedups proves two commits whose fragment never
// changed collapse to a single item (same effective hash ⇒ one).
func TestMultiVersion_IdenticalContentDedups(t *testing.T) {
	def := &Bundle{Fragments: map[string]BundleFragment{"solid": {Content: "default body"}}}
	versions := map[string]*Bundle{
		"c1": {Fragments: map[string]BundleFragment{"solid": {Content: "same body"}}},
		"c2": {Fragments: map[string]BundleFragment{"solid": {Content: "same body"}}},
	}
	gate := hashGate(map[string]bool{effHash("same body"): true})
	l := versionedLoader(t, cqRef, def, versions, gate)

	got := l.ResolveFragmentVersions(cqFrag, []string{"c1", "c2"})
	if len(got) != 1 {
		t.Fatalf("got %d versions, want 1 (identical content dedups)", len(got))
	}
	if got[0].Content != "same body" {
		t.Errorf("content = %q, want %q", got[0].Content, "same body")
	}
}

// TestMultiVersion_FetchFailureWithholdsOnlyThatVersion proves a per-version
// resolve failure drops ONLY that version (fail-closed) while the others resolve.
func TestMultiVersion_FetchFailureWithholdsOnlyThatVersion(t *testing.T) {
	def := &Bundle{Fragments: map[string]BundleFragment{"solid": {Content: "default body"}}}
	versions := map[string]*Bundle{
		"c1": {Fragments: map[string]BundleFragment{"solid": {Content: "v1 body"}}},
		// "broken" is intentionally absent ⇒ the fake resolver errors.
	}
	gate := hashGate(map[string]bool{effHash("v1 body"): true})
	l := versionedLoader(t, cqRef, def, versions, gate)

	got := l.ResolveFragmentVersions(cqFrag, []string{"c1", "broken"})
	if len(got) != 1 {
		t.Fatalf("got %d versions, want 1 (broken version dropped)", len(got))
	}
	if got[0].Content != "v1 body" {
		t.Errorf("content = %q, want %q", got[0].Content, "v1 body")
	}

	// Direct: a fetch failure surfaces as a resolve error, never a panic.
	if _, err := l.GetFragmentAtVersion(cqFrag, "broken"); err == nil {
		t.Error("GetFragmentAtVersion(broken) err = nil, want resolve failure")
	}
}

// TestMultiVersion_DefaultPathUnchanged proves the no-@commit path is identical
// to the lockfile-pinned default: GetFragment and an empty-commit resolution both
// return the seeded default, never a historical version.
func TestMultiVersion_DefaultPathUnchanged(t *testing.T) {
	def := &Bundle{Fragments: map[string]BundleFragment{"solid": {Content: "default body"}}}
	versions := map[string]*Bundle{
		"c1": {Fragments: map[string]BundleFragment{"solid": {Content: "v1 body"}}},
	}
	gate := hashGate(map[string]bool{
		effHash("default body"): true,
		effHash("v1 body"):      true,
	})
	l := versionedLoader(t, cqRef, def, versions, gate)

	// Plain GetFragment is the untouched lockfile path.
	lc, err := l.GetFragment(cqFrag)
	if err != nil {
		t.Fatalf("GetFragment: %v", err)
	}
	if lc.Content != "default body" {
		t.Errorf("GetFragment content = %q, want default", lc.Content)
	}

	// Empty commit ⇒ same default.
	def2 := l.ResolveFragmentVersions(cqFrag, []string{""})
	if len(def2) != 1 || def2[0].Content != "default body" {
		t.Errorf("empty-commit resolution = %v, want [default body]", def2)
	}

	// An explicit commit DOES diverge to the historical version.
	v1, err := l.GetFragmentAtVersion(cqFrag, "c1")
	if err != nil {
		t.Fatalf("GetFragmentAtVersion(c1): %v", err)
	}
	if v1.Content != "v1 body" {
		t.Errorf("c1 content = %q, want v1 body", v1.Content)
	}
}

// TestMultiVersion_EmbeddedCommitAddressing proves the <ref>@<commit> string form
// resolves the historical version (commit carried on the bundle part of the ref).
func TestMultiVersion_EmbeddedCommitAddressing(t *testing.T) {
	def := &Bundle{Fragments: map[string]BundleFragment{"solid": {Content: "default body"}}}
	versions := map[string]*Bundle{
		"c1": {Fragments: map[string]BundleFragment{"solid": {Content: "v1 body"}}},
	}
	gate := hashGate(map[string]bool{effHash("v1 body"): true})
	l := versionedLoader(t, cqRef, def, versions, gate)

	// "<ref>@<commit>" with no explicit commit arg pins via the embedded version.
	lc, err := l.GetFragmentAtVersion(cqRef+"@c1#fragments/solid", "")
	if err != nil {
		t.Fatalf("GetFragmentAtVersion(embedded c1): %v", err)
	}
	if lc.Content != "v1 body" {
		t.Errorf("embedded-commit content = %q, want v1 body", lc.Content)
	}
}

// TestMultiVersion_NoResolverFailsClosed proves that without a version resolver a
// pinned-commit request fails closed (ErrNoVersionResolver) while the default
// (lockfile) path keeps working.
func TestMultiVersion_NoResolverFailsClosed(t *testing.T) {
	def := &Bundle{Fragments: map[string]BundleFragment{"solid": {Content: "default body"}}}
	l := ungated(NewLoader(seedLocal(map[string]*Bundle{cqRef: def})), true)

	if _, err := l.GetFragmentAtVersion(cqFrag, "c1"); !errors.Is(err, errs.ErrNoVersionResolver) {
		t.Errorf("GetFragmentAtVersion without resolver err = %v, want ErrNoVersionResolver", err)
	}
	// Default path still resolves.
	lc, err := l.GetFragment(cqFrag)
	if err != nil || lc.Content != "default body" {
		t.Errorf("default GetFragment = (%v, %v), want default body", lc, err)
	}
}

// TestMultiVersion_TypedSourceRefIsStamped proves a version-pinned read's
// typed SourceRef is stamped, not left zero. This is the silent-withholding
// hazard bundleAtVersion used to carry: BundleRead.SourceRef reports the zero
// BundleRef for ANY read whose sourceRefTyped was never set, and a producer
// that mints an item ref from a zero source (WithItem on an empty Bundle)
// fails — silently withholding every item the version-pinned read serves,
// with only a warn line to show for it. bundleAtVersion must stamp
// sourceRefTyped through the SAME canonicalBundleRefTyped bridge
// repoFSReader.sourceRefTyped uses on a ref of this identical canonical
// shape, so a historical version keys under the SAME trust identity as its
// unpinned twin.
func TestMultiVersion_TypedSourceRefIsStamped(t *testing.T) {
	def := &Bundle{Fragments: map[string]BundleFragment{"solid": {Content: "default body"}}}
	versions := map[string]*Bundle{
		"c1": {Fragments: map[string]BundleFragment{"solid": {Content: "v1 body"}}},
	}
	gate := hashGate(map[string]bool{effHash("v1 body"): true})
	l := versionedLoader(t, cqRef, def, versions, gate)

	read, err := l.loader.bundleAtVersion(cqRef, "c1")
	if err != nil {
		t.Fatalf("bundleAtVersion: %v", err)
	}
	want, err := trust.GitRef("github.com", "/acme/b", "cq")
	if err != nil {
		t.Fatalf("trust.GitRef: %v", err)
	}
	if got := read.SourceRef(); got != want {
		t.Errorf("SourceRef() = %+v, want %+v (a version-pinned read must be addressable, or its items are silently withheld)", got, want)
	}
}

// TestMultiVersion_Prompt covers the prompt counterpart: a historical prompt
// version is gated by its own effective hash under the version-less ref.
func TestMultiVersion_Prompt(t *testing.T) {
	promptRef := cqRef + "#commands/review"
	def := &Bundle{Commands: map[string]BundleCommand{"review": {Content: "default review"}}}
	versions := map[string]*Bundle{
		"c1": {Commands: map[string]BundleCommand{"review": {Content: "v1 review"}}},
		"c2": {Commands: map[string]BundleCommand{"review": {Content: "v2 review"}}},
	}
	gate := hashGate(map[string]bool{effHash("v1 review"): true})
	l := versionedLoader(t, cqRef, def, versions, gate)

	v1, err := l.GetPromptAtVersion(promptRef, "c1")
	if err != nil {
		t.Fatalf("GetPromptAtVersion(c1): %v", err)
	}
	if v1.Content != "v1 review" {
		t.Errorf("c1 prompt = %q, want v1 review", v1.Content)
	}
	if _, err := l.GetPromptAtVersion(promptRef, "c2"); !errors.Is(err, errs.ErrCommandWithheld) {
		t.Errorf("GetPromptAtVersion(c2) err = %v, want ErrCommandWithheld", err)
	}
}
