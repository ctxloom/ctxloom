package trust

import "testing"

func TestCanonicalRepoURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"https passthrough", "https://github.com/acme/repo", "https://github.com/acme/repo"},
		{"strip .git", "https://github.com/acme/repo.git", "https://github.com/acme/repo"},
		{"lowercase github owner/repo", "https://github.com/Acme/Repo", "https://github.com/acme/repo"},
		{"lowercase host", "https://GitHub.com/acme/repo", "https://github.com/acme/repo"},
		{"trailing slash", "https://github.com/acme/repo/", "https://github.com/acme/repo"},
		{"git@ to https", "git@github.com:acme/repo.git", "https://github.com/acme/repo"},
		{"git@ mixed case", "git@github.com:Acme/Repo", "https://github.com/acme/repo"},
		{"shorthand owner/repo", "acme/repo", "https://github.com/acme/repo"},
		{"empty", "", ""},
		{"local token passthrough", "ctxloom:local", "ctxloom:local"},
		// Without this special case, remote.NormalizeURL's "no scheme, no
		// slash" fallback would mangle "ctxloom:companion" into
		// "https://ctxloom:companion" — the exact bug the ctxloom:local case
		// above exists to avoid, and a companion loadout ref's whole reason
		// for being recognized/gated (not local, not unrecognized) depends on
		// its RepoURL canonicalizing to itself.
		{"companion token passthrough", "ctxloom:companion", "ctxloom:companion"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalRepoURL(tt.in); got != tt.want {
				t.Errorf("CanonicalRepoURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestCanonicalRepoURL_VariantsCollapse pins the property the blacklist relies
// on: every spelling of the same GitHub repo canonicalizes to one key.
func TestCanonicalRepoURL_VariantsCollapse(t *testing.T) {
	variants := []string{
		"https://github.com/Acme/Repo",
		"https://github.com/acme/repo.git",
		"https://GitHub.com/acme/repo/",
		"git@github.com:acme/repo",
		"git@github.com:Acme/Repo.git",
		"acme/repo",

		// Each of these escaped a sticky ref-level rejection by
		// respelling the remote URL — and for a bundle carrying a verified
		// publisher signature the escape is not "rejected -> pending" but
		// "rejected -> ALLOW" at step 5, because the store address derives
		// from this canonical form and any divergence is simply a store miss.
		// docs/trust-model.md lists "URL-variant / typosquat escape of a
		// rejection" as an ADDRESSED threat.
		"https://github.com/acme/repo.git/",   // NormalizeURL strips .git BEFORE the trailing slash is trimmed
		"git@github.com:acme/repo.git/",       // same, via the git@ rewrite
		"HTTPS://github.com/acme/repo",        // the http(s) guard was case-SENSITIVE, so this skipped folding entirely
		"https://www.github.com/acme/repo",    // www. was in knownCaseFoldForges but never folded off
		"http://github.com/acme/repo",         // scheme downgrade
		"https://user@github.com/acme/repo",   // userinfo
		"https://github.com/acme/repo?ref=x",  // query
		"https://github.com/acme/repo#readme", // fragment
		"https://github.com/acme/repo//",      // repeated trailing slashes
	}
	want := CanonicalRepoURL(variants[0])
	for _, v := range variants {
		if got := CanonicalRepoURL(v); got != want {
			t.Errorf("CanonicalRepoURL(%q) = %q, want %q (variant must collapse)", v, got, want)
		}
	}
}

func TestRefKey(t *testing.T) {
	tests := []struct {
		ref  Ref
		want string
	}{
		{Ref{Bundle: "code-quality", Kind: KindFragment, Name: "solid"}, "code-quality#fragments/solid"},
		{Ref{Bundle: "tooling", Kind: KindPrompt, Name: "review"}, "tooling#prompts/review"},
		{Ref{Bundle: "tooling", Kind: KindMCP, Name: "postgres"}, "tooling#mcp/postgres"},
	}
	for _, tt := range tests {
		if got := tt.ref.Key(); got != tt.want {
			t.Errorf("Ref%+v.Key() = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestRefCanonicalURL_Local(t *testing.T) {
	r := Ref{IsLocal: true, Bundle: "dev", Kind: KindFragment, Name: "x"}
	if got := r.CanonicalURL(); got != "ctxloom:local" {
		t.Errorf("local Ref.CanonicalURL() = %q, want %q", got, "ctxloom:local")
	}
}

func TestItemKind_IsContent(t *testing.T) {
	if !KindFragment.IsContent() || !KindPrompt.IsContent() {
		t.Error("fragment and prompt must be content")
	}
	if KindMCP.IsContent() {
		t.Error("mcp must NOT be content (executable surface, never auto-trusted)")
	}
}
