package refuri

import (
	"errors"
	"testing"
)

func TestCanonicalRepoURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"https passthrough", "https://github.com/acme/repo", "https://github.com/acme/repo"},
		{".git suffix PRESERVED", "https://github.com/acme/repo.git", "https://github.com/acme/repo.git"},
		{"repo-path case PRESERVED", "https://github.com/Acme/Repo", "https://github.com/Acme/Repo"},
		{"lowercase host", "https://GitHub.com/acme/repo", "https://github.com/acme/repo"},
		{"trailing slash", "https://github.com/acme/repo/", "https://github.com/acme/repo"},
		{"git@ to https, suffix intact", "git@github.com:acme/repo.git", "https://github.com/acme/repo.git"},
		{"git@ preserves repo-path case", "git@github.com:Acme/Repo", "https://github.com/Acme/Repo"},
		{"shorthand owner/repo", "acme/repo", "https://github.com/acme/repo"},
		{"local token passthrough", "ctxloom:local", "ctxloom:local"},
		{"companion token passthrough", "ctxloom:companion", "ctxloom:companion"},
		{"file path escaped", "file:///srv/has space/a%2541b", "file:///srv/has%20space/a%2541b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalRepoURL(tt.in)
			if err != nil {
				t.Fatalf("CanonicalRepoURL(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("CanonicalRepoURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A string this cannot read as a repository is an ERROR, never echoed back:
// a verbatim fallback mints a key for a string nobody can address.
func TestCanonicalRepoURL_UnreadableIsAnError(t *testing.T) {
	for _, in := range []string{"", "https://", "https://github.com/acme/%zz", "https://github.com", "file:///"} {
		got, err := CanonicalRepoURL(in)
		if !errors.Is(err, ErrSyntax) {
			t.Errorf("CanonicalRepoURL(%q) = %q, %v; want an ErrSyntax error", in, got, err)
		}
	}
}

// The spellings that are the SAME repository — RFC 3986 §6.2 case and
// trailing-slash normalization, the request-addressing components that never
// name one (userinfo, query, fragment), and the transport (scp, http) —
// collapse to one key.
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
		"ssh://git@GitHub.com/acme/repo",
	}
	want, err := CanonicalRepoURL(variants[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range variants {
		if got, err := CanonicalRepoURL(v); err != nil || got != want {
			t.Errorf("CanonicalRepoURL(%q) = %q, %v; want %q (variant must collapse)", v, got, err, want)
		}
	}
}

// The inverse: a spelling that is merely non-preferred is a DIFFERENT
// identity. Whether two addresses reach one repository is host-specific
// knowledge this layer does not have.
func TestCanonicalRepoURL_NonPreferredSpellingsStayDISTINCT(t *testing.T) {
	base, err := CanonicalRepoURL("https://github.com/acme/repo")
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{
		"https://github.com/acme/repo.git",
		"https://github.com/Acme/Repo",
		"https://www.github.com/acme/repo",
	} {
		if got, _ := CanonicalRepoURL(in); got == base {
			t.Errorf("CanonicalRepoURL(%q) = %q, which collapsed onto %q — two identities merged onto one trust key", in, got, base)
		}
	}
}

// FetchURL is read back by ParseRepoIdentity to the same repository, for a
// path carrying the characters that need escaping: a renderer whose output
// does not parse back to its input is a second identity.
func TestFetchURL_RoundTripsThroughParseRepoIdentity(t *testing.T) {
	for _, p := range []Parts{
		{Class: ClassFile, RepoPath: "/srv/has space/a%41b"},
		{Class: ClassGit, Host: "example.test", RepoPath: "/o/has space/a%41b"},
	} {
		got, err := ParseRepoIdentity(p.FetchURL())
		if err != nil {
			t.Fatalf("ParseRepoIdentity(%q): %v", p.FetchURL(), err)
		}
		if got.Class != p.Class || got.Host != p.Host || got.RepoPath != p.RepoPath {
			t.Errorf("FetchURL %q parsed back to %+v, want %+v", p.FetchURL(), got, p)
		}
	}
}

// A repository path containing the "//" separator cannot be rendered into a
// reference without the next parse splitting it there: "o//bundles/r" would
// come back as repository "o". Refused, never re-keyed as another repository.
func TestParseRepoIdentity_RefusesTheSeparatorInsideARepoPath(t *testing.T) {
	for _, in := range []string{
		"https://example.test/o//bundles/r",
		"https://example.test/o//r",
		"file:///srv//bundles/r",
		"https://example.test/o/.//bundles/r",
	} {
		got, err := ParseRepoIdentity(in)
		if !errors.Is(err, ErrSyntax) {
			t.Errorf("ParseRepoIdentity(%q) = %+v, %v; want an ErrSyntax error", in, got, err)
		}
	}
}

// "%2F" decodes to a structural "/", so "a%2Fb" and "a/b" would be one
// repository. ParseBundleRef refuses that escape; so must the repo parse, or
// the two canonicalizers disagree about which repositories exist.
func TestParseRepoIdentity_RefusesAnEncodedSlash(t *testing.T) {
	for _, in := range []string{
		"https://h/o/a%2Fb/r",
		"https://h/o/a%2fb/r",
		"file:///srv/a%2Fb/r",
		"file:///srv/has space/a%2Fb/r",
	} {
		got, err := ParseRepoIdentity(in)
		if !errors.Is(err, ErrSyntax) {
			t.Errorf("ParseRepoIdentity(%q) = %+v, %v; want an ErrSyntax error", in, got, err)
		}
	}
}
