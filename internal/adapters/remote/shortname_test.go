package remote

import "testing"

// aliasToPersonal is the test alias resolver: "personal" is a configured remote,
// everything else is unknown.
func aliasToPersonal(alias string) string {
	if alias == "personal" {
		return "https://github.com/ben/ctxloom-personal"
	}
	return ""
}

// personalBundles is the canonical ctxloom URI prefix of the "personal"
// remote's bundles: the one spelling CanonicalizeShortRef emits for it.
const personalBundles = "ctxloom+git://github.com/ben/ctxloom-personal//bundles/"

// TestCanonicalizeShortRef pins the short-name → canonical grammar: bare names
// stay local, "<remote>/<bundle>" (with or without a selector) expands to the
// canonical ctxloom URI, a self-contained fetch-address ref is re-spelled in that
// same grammar, the local file wins a collision, and any selector rides through
// unchanged. Mirrors internal/core/profiles/grammar_test.go's spelling→identity pins.
func TestCanonicalizeShortRef(t *testing.T) {
	tests := []struct {
		name        string
		ref         string
		localExists func(string) bool
		want        string
	}{
		// Decision A/C: a bare, unprefixed name is always local.
		{"bare name is local", "core-practices", nil, "core-practices"},
		{"bare name with fragment selector is local", "core-practices#fragments/tdd", nil, "core-practices#fragments/tdd"},
		{"bare name with profile selector is local", "tools#profiles/probe", nil, "tools#profiles/probe"},

		// "<remote>/<bundle>[#<sel>/<item>]" → canonical URL, selector preserved.
		{"remote bundle plain", "personal/agent-ensemble", nil,
			personalBundles + "agent-ensemble"},
		{"remote bundle with profile selector", "personal/agent-ensemble#profiles/finder", nil,
			personalBundles + "agent-ensemble#profiles/finder"},
		{"remote bundle with fragment selector", "personal/agent-ensemble#fragments/tdd", nil,
			personalBundles + "agent-ensemble#fragments/tdd"},
		{"selector carries a version pin through", "personal/agent-ensemble#profiles/finder@abc1234", nil,
			personalBundles + "agent-ensemble#profiles/finder@abc1234"},
		{"nested bundle path keeps its slashes", "personal/lang/go#profiles/dev", nil,
			personalBundles + "lang/go#profiles/dev"},

		// Decision E: local-file-wins over a same-spelled remote alias.
		{"local file wins the collision", "personal/agent-ensemble#profiles/finder",
			func(base string) bool { return base == "personal/agent-ensemble" },
			"personal/agent-ensemble#profiles/finder"},

		// Unknown alias / self-contained refs need no registry.
		{"unknown alias stays as authored", "work/agent-ensemble#profiles/finder", nil,
			"work/agent-ensemble#profiles/finder"},
		{"fetch-address ref is re-spelled canonically", "https://github.com/x/y@bundles/z#profiles/p", nil,
			"ctxloom+git://github.com/x/y//bundles/z#profiles/p"},
		{"canonical URI is self-contained", "ctxloom+git://github.com/x/y//bundles/z#profiles/p", nil,
			"ctxloom+git://github.com/x/y//bundles/z#profiles/p"},
		{"ctxloom:local is self-contained", "ctxloom:local@bundles/dev", nil,
			"ctxloom:local@bundles/dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanonicalizeShortRef(tt.ref, aliasToPersonal, tt.localExists)
			if got != tt.want {
				t.Fatalf("CanonicalizeShortRef(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

// TestCanonicalizeShortRef_NilResolver pins fault tolerance: with no alias
// resolver every short "<remote>/<bundle>" ref is left as authored rather than
// dropped, so a missing registry degrades to "resolve nothing" not "corrupt".
func TestCanonicalizeShortRef_NilResolver(t *testing.T) {
	ref := "personal/agent-ensemble#profiles/finder"
	if got := CanonicalizeShortRef(ref, nil, nil); got != ref {
		t.Fatalf("nil resolver: got %q, want %q unchanged", got, ref)
	}
}

// TestCanonicalizeProfileShortRef pins the profile-ref guard: only a
// "#profiles/"-carrying alias ref canonicalizes; a selector-less two-segment name
// stays the local subdir profile it names (never resolves against a remote).
func TestCanonicalizeProfileShortRef(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{"selector-less two-segment name stays local", "personal/go-developer", "personal/go-developer"},
		{"bare local profile name stays local", "developer", "developer"},
		{"local bundle profile stays local", "tools#profiles/probe", "tools#profiles/probe"},
		{"alias bundle profile canonicalizes", "personal/agent-ensemble#profiles/finder",
			personalBundles + "agent-ensemble#profiles/finder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanonicalizeProfileShortRef(tt.ref, aliasToPersonal)
			if got != tt.want {
				t.Fatalf("CanonicalizeProfileShortRef(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}
