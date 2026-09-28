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

// Scheme-default ports and IDN hosts are the two remaining spellings that are
// PROVABLY one repository: RFC 3986 §6.2.3 makes an explicit default port
// equivalent to none, and an IDNA U-label and its A-label are one DNS name.
// Each row is a spelling and the one canonical key it must fold to.
func TestCanonicalRepoURL_DefaultPortAndIDNFold(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"https :443 dropped", "https://host.example:443/o/r", "https://host.example/o/r"},
		{"http :80 dropped", "http://host.example:80/o/r", "https://host.example/o/r"},
		{"ssh :22 dropped", "ssh://git@host.example:22/o/r", "https://host.example/o/r"},
		{"git+ssh :22 dropped", "git+ssh://git@host.example:22/o/r", "https://host.example/o/r"},
		{"ssh+git :22 dropped", "ssh+git://git@host.example:22/o/r", "https://host.example/o/r"},
		{"git :9418 dropped", "git://host.example:9418/o/r", "https://host.example/o/r"},
		{"empty port dropped", "https://host.example:/o/r", "https://host.example/o/r"},
		{"non-default port kept", "https://host.example:8443/o/r", "https://host.example:8443/o/r"},
		{"another scheme's default is not this one's", "https://host.example:22/o/r", "https://host.example:22/o/r"},
		{"ssh non-default port kept", "ssh://git@host.example:2222/o/r", "https://host.example:2222/o/r"},
		{"IDN host to A-label", "https://bücher.example/o/r", "https://xn--bcher-kva.example/o/r"},
		{"IDN host, mixed case", "https://BÜCHER.Example/o/r", "https://xn--bcher-kva.example/o/r"},
		{"IDN host, scp form", "git@bücher.example:o/r", "https://xn--bcher-kva.example/o/r"},
		{"IDN host, scheme-less host path", "bücher.example/o/r", "https://xn--bcher-kva.example/o/r"},
		{"IDN host and default port", "https://bücher.example:443/o/r", "https://xn--bcher-kva.example/o/r"},
		{"IDN host, non-default port kept", "https://bücher.example:8443/o/r", "https://xn--bcher-kva.example:8443/o/r"},
		{"A-label passthrough", "https://xn--bcher-kva.example/o/r", "https://xn--bcher-kva.example/o/r"},
		{"ASCII host lowercased", "https://Host.Example/o/r", "https://host.example/o/r"},
		{"IPv6 literal default port dropped", "https://[::1]:443/o/r", "https://[::1]/o/r"},
		{"IPv6 literal non-default port kept", "https://[::1]:8443/o/r", "https://[::1]:8443/o/r"},
		{".git suffix still PRESERVED", "https://host.example:443/o/r.git", "https://host.example/o/r.git"},
		{"repo-path case still PRESERVED", "https://bücher.example/O/R", "https://xn--bcher-kva.example/O/R"},
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

// The canonical key is a FIXED POINT: trust.RefFromBundleRef renders an
// identity with FetchURL and ParseRepoIdentity reads it back, so a key that
// re-canonicalizes to a different key is two keys for one repository. The
// hazard is a non-default SOURCE port that happens to be https's default
// (ssh on :443), which renders as https.
func TestCanonicalRepoURL_IsAFixedPoint(t *testing.T) {
	for _, in := range []string{
		"ssh://git@host.example:443/o/r",
		"http://host.example:443/o/r",
		"https://host.example:80/o/r",
		"git@bücher.example:o/r",
		"https://bücher.example:8443/o/r",
	} {
		once, err := CanonicalRepoURL(in)
		if err != nil {
			t.Fatalf("CanonicalRepoURL(%q): %v", in, err)
		}
		twice, err := CanonicalRepoURL(once)
		if err != nil || twice != once {
			t.Errorf("CanonicalRepoURL(%q) = %q, but that re-canonicalizes to %q, %v", in, once, twice, err)
		}
	}
}

// A host IDNA refuses under the lookup profile is an ERROR, never passed
// through: a key minted for a name DNS cannot resolve addresses nothing, and
// a verbatim Unicode key would sit beside its A-label twin as a second one.
func TestCanonicalRepoURL_RefusesAnInvalidIDNHost(t *testing.T) {
	for _, in := range []string{
		"https://xn--bcher-kva-.example/o/r", // punycode that does not decode
		"https://a⒈.example/o/r",             // ⒈ is disallowed under STD3 rules
		"git@bad_host.example:o/r",           // "_" is not a host-name character
	} {
		got, err := CanonicalRepoURL(in)
		if !errors.Is(err, ErrSyntax) {
			t.Errorf("CanonicalRepoURL(%q) = %q, %v; want an ErrSyntax error", in, got, err)
		}
	}
}

// A hand-written bundle reference folds the same way: the ref grammar, not
// only the repo-URL front door, owns host canonicalization, so a reference and
// a repository URL naming one repository cannot key apart.
func TestParse_GitHostFoldsIDNAndHTTPSDefaultPort(t *testing.T) {
	want, err := Parse("ctxloom+git://xn--bcher-kva.example/o//bundles/b")
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{
		"ctxloom+git://bücher.example/o//bundles/b",
		"ctxloom+git://BÜCHER.example:443/o//bundles/b",
	} {
		got, err := Parse(in)
		if err != nil || got.Render(true) != want.Render(true) {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got.Render(true), err, want.Render(true))
		}
	}
}
