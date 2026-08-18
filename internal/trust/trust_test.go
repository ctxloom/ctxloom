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
		{"repo-path case PRESERVED", "https://github.com/Acme/Repo", "https://github.com/Acme/Repo"},
		{"lowercase host", "https://GitHub.com/acme/repo", "https://github.com/acme/repo"},
		{"trailing slash", "https://github.com/acme/repo/", "https://github.com/acme/repo"},
		{"git@ to https", "git@github.com:acme/repo.git", "https://github.com/acme/repo"},
		{"git@ preserves repo-path case", "git@github.com:Acme/Repo", "https://github.com/Acme/Repo"},
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

// TestCanonicalRepoURL_ConformantVariantsCollapse pins the spellings that are
// the SAME URI under RFC 3986 §6.2 (scheme and host case, a trailing slash)
// plus the two request-addressing components that never name a repository
// (userinfo, query, fragment) — and the transport unification NormalizeURL
// owns (git@ scp form, and http, which no forge serves differently from
// https).
func TestCanonicalRepoURL_ConformantVariantsCollapse(t *testing.T) {
	variants := []string{
		"https://github.com/acme/repo",
		"https://GitHub.com/acme/repo/",
		"HTTPS://github.com/acme/repo",
		"http://github.com/acme/repo",
		"git@github.com:acme/repo",
		"acme/repo",
		"https://user@github.com/acme/repo",
		"https://github.com/acme/repo?ref=x",
		"https://github.com/acme/repo#readme",
		"https://github.com/acme/repo//",
	}
	want := CanonicalRepoURL(variants[0])
	for _, v := range variants {
		if got := CanonicalRepoURL(v); got != want {
			t.Errorf("CanonicalRepoURL(%q) = %q, want %q (variant must collapse)", v, got, want)
		}
	}
}

// TestCanonicalRepoURL_NonPreferredSpellingsStayDISTINCT is the inverse, and
// it is the property the byte-exact rule exists for: a spelling that is merely
// non-preferred is a DIFFERENT identity, not a variant. Whether two addresses
// reach the same repository is host-specific knowledge this layer does not
// have. A rejection of one does not govern the other — that is what a
// CONTENT-reject is for, and it omits the ref by design.
func TestCanonicalRepoURL_NonPreferredSpellingsStayDISTINCT(t *testing.T) {
	base := CanonicalRepoURL("https://github.com/acme/repo")
	for _, tt := range []struct{ name, in string }{
		{"repository-path case", "https://github.com/Acme/Repo"},
		{"www. host", "https://www.github.com/acme/repo"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalRepoURL(tt.in); got == base {
				t.Errorf("CanonicalRepoURL(%q) = %q, which collapsed onto %q — two identities merged onto one trust key", tt.in, got, base)
			}
		})
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
