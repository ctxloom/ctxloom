package bundles

import (
	"errors"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// blockingGate withholds any item whose ref contains one of substrs, recording
// the (hash, form) of every item it sees — hashing the PAYLOAD the gate is now
// handed — so a test can still assert the gate is fed the exact bytes about to
// be exposed.
func blockingGate(seen map[string][2]string, substrs ...string) ContentGate {
	return func(ref string, payload []byte, form, _signer string) bool {
		if seen != nil {
			seen[ref] = [2]string{HashPayload(payload), form}
		}
		for _, s := range substrs {
			if strings.Contains(ref, s) {
				return false
			}
		}
		return true
	}
}

func demoSeed() map[string]*Bundle {
	seed := map[string]*Bundle{
		"demo": {
			Fragments: map[string]BundleFragment{
				"keep":    {Content: "keep body"},
				"blocked": {Content: "blocked body"},
			},
			Commands: map[string]BundleCommand{
				"okprompt":  {Content: "ok prompt body"},
				"badprompt": {Content: "bad prompt body"},
			},
		},
	}
	for k, b := range seed {
		b.Name = k
	}
	return seed
}

// TestLoaderGate_WithholdsFragment_KeepsSibling proves the choke (fragmentContent)
// drops a denied fragment from GetFragment (so the ctxloom:// resource omits it)
// while a trusted sibling still resolves, and records the withheld ref.
func TestLoaderGate_WithholdsFragment_KeepsSibling(t *testing.T) {
	l := NewLoader(nil, WithSeededBundles(demoSeed()),
		WithTrustGate(blockingGate(nil, "#fragments/blocked")))

	if _, err := l.GetFragment("demo#fragments/blocked", true); !errors.Is(err, errs.ErrFragmentWithheld) {
		t.Fatalf("GetFragment(blocked) err = %v, want ErrFragmentWithheld", err)
	}
	got, err := l.GetFragment("demo#fragments/keep", true)
	if err != nil {
		t.Fatalf("GetFragment(keep): %v", err)
	}
	if got.Content != "keep body" {
		t.Errorf("keep content = %q, want %q", got.Content, "keep body")
	}
	if w := l.Withheld(); len(w) != 1 || w[0] != "demo#fragments/blocked" {
		t.Errorf("Withheld() = %v, want [demo#fragments/blocked]", w)
	}
}

// TestLoaderGate_WithholdsCommand proves the command choke (commandContent)
// drops a denied prompt (ErrCommandWithheld) while a trusted one resolves. The
// trust gate ref keeps the "#prompts/" kind segment (trust.KindPrompt.Dir())
// even though the load selector is "#commands/" — the item-kind rename does
// not re-key trust.
func TestLoaderGate_WithholdsCommand(t *testing.T) {
	l := NewLoader(nil, WithSeededBundles(demoSeed()),
		WithTrustGate(blockingGate(nil, "#prompts/badprompt")))

	if _, err := l.GetCommand("demo#commands/badprompt", true); !errors.Is(err, errs.ErrCommandWithheld) {
		t.Fatalf("GetCommand(badprompt) err = %v, want ErrCommandWithheld", err)
	}
	if _, err := l.GetCommand("demo#commands/okprompt", true); err != nil {
		t.Fatalf("GetCommand(okprompt): %v", err)
	}
	if w := l.Withheld(); len(w) != 1 || w[0] != "demo#prompts/badprompt" {
		t.Errorf("Withheld() = %v, want [demo#prompts/badprompt]", w)
	}
}

// TestLoaderGate_PassesEffectiveHash proves the gate keys on the effective-content
// hash of the EXACT bytes exposed: a distilled fragment (preferDistilled true)
// must be gated on its distilled hash + form, never the raw content.
func TestLoaderGate_PassesEffectiveHash(t *testing.T) {
	frag := BundleFragment{Content: "raw body", Distilled: "distilled body"}
	seed := map[string]*Bundle{"b": {Name: "b", Fragments: map[string]BundleFragment{"f": frag}}}
	seen := map[string][2]string{}
	l := NewLoader(nil, WithSeededBundles(seed), WithTrustGate(blockingGate(seen)))

	got, err := l.GetFragment("b#fragments/f", true)
	if err != nil {
		t.Fatalf("GetFragment: %v", err)
	}
	if got.Content != "distilled body" {
		t.Fatalf("exposed content = %q, want distilled", got.Content)
	}
	wantHash, wantForm := frag.EffectiveContentHash(true)
	hf, ok := seen["b#fragments/f"]
	if !ok {
		t.Fatal("gate was never called for b#fragments/f")
	}
	if hf[0] != wantHash || hf[1] != string(wantForm) {
		t.Errorf("gate saw {hash:%s form:%s}, want {%s %s}", hf[0], hf[1], wantHash, wantForm)
	}
	if wantForm != FormDistilled {
		t.Errorf("expected distilled form, got %s", wantForm)
	}
}

// TestLoaderGate_SeededBundleGatesByCanonicalRef proves a cloned (seeded) bundle's
// content is gated by its CANONICAL source ref — the seed key — not the short
// bundle.Name. So the trust cascade reads a clone's TEXT as IsLocal=false and it
// follows the remote's trust, exactly like the clone's executables ("text to an
// LLM is executable"). A project (fs) bundle keeps keying by its local name (the
// other gate tests, whose seed keys equal the short name, cover that path).
func TestLoaderGate_SeededBundleGatesByCanonicalRef(t *testing.T) {
	const canonical = "https://github.com/acme/repo@bundles/tooling"
	seed := map[string]*Bundle{canonical: {Name: "tooling", Fragments: map[string]BundleFragment{"f": {Content: "body"}}}}
	seen := map[string][2]string{}
	l := NewLoader(nil, WithSeededBundles(seed), WithTrustGate(blockingGate(seen)))

	if _, err := l.GetFragment(canonical+"#fragments/f", true); err != nil {
		t.Fatalf("GetFragment: %v", err)
	}
	if _, ok := seen[canonical+"#fragments/f"]; !ok {
		t.Errorf("gate not fed the canonical source ref; saw %v", seen)
	}
	if _, ok := seen["tooling#fragments/f"]; ok {
		t.Error("cloned content gated by short bundle.Name — would read as local (a trust hole)")
	}
}

// TestLoaderGate_Search_PrefersTrustedSibling proves a bare-name search skips a
// withheld match and returns a trusted copy from another bundle; only when every
// match is withheld does it report ErrFragmentWithheld (not not-found).
func TestLoaderGate_Search_PrefersTrustedSibling(t *testing.T) {
	seed := map[string]*Bundle{
		"a-bundle": {Name: "a-bundle", Fragments: map[string]BundleFragment{"shared": {Content: "from a"}}},
		"z-bundle": {Name: "z-bundle", Fragments: map[string]BundleFragment{"shared": {Content: "from z"}}},
	}
	// Block only a-bundle; the z-bundle copy must win the bare-name search.
	l := NewLoader(nil, WithSeededBundles(seed), WithTrustGate(blockingGate(nil, "a-bundle#")))
	got, err := l.GetFragment("shared", true)
	if err != nil {
		t.Fatalf("search GetFragment(shared): %v", err)
	}
	if got.Content != "from z" {
		t.Errorf("search returned %q, want the trusted z-bundle copy", got.Content)
	}

	// Block both → all matches withheld → ErrFragmentWithheld (distinct from not-found).
	l2 := NewLoader(nil, WithSeededBundles(seed), WithTrustGate(blockingGate(nil, "#fragments/shared")))
	if _, err := l2.GetFragment("shared", true); !errors.Is(err, errs.ErrFragmentWithheld) {
		t.Errorf("all-withheld search err = %v, want ErrFragmentWithheld", err)
	}
}

// TestLoaderGate_NilGate_ExposesEverything proves a gate-free loader (management/
// listing path) is unaffected: content resolves and nothing is recorded withheld.
func TestLoaderGate_NilGate_ExposesEverything(t *testing.T) {
	l := NewLoader(nil, WithSeededBundles(demoSeed()))
	if _, err := l.GetFragment("demo#fragments/blocked", true); err != nil {
		t.Fatalf("nil-gate GetFragment must expose: %v", err)
	}
	if w := l.Withheld(); len(w) != 0 {
		t.Errorf("nil-gate Withheld() = %v, want empty", w)
	}
}
