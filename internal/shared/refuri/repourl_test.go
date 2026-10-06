package refuri

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// A scheme-less absolute path is a LOCAL repository, and the grammar has no
// way to tell it from a host-qualified path once the leading "/" is trimmed:
// "/srv/bundles.git" would otherwise be guessed into
// "https://github.com/srv/bundles" — a real, fetchable, trust-keyed URL the
// user never named. It must be refused, and the refusal must carry the file://
// spelling that does work.
func TestParseRepoURL_RefusesASchemelessAbsolutePath(t *testing.T) {
	for _, in := range []string{"/srv/bundles.git", "/srv/bundles", "/home/me/repo/", "  /srv/x.git  "} {
		got, err := ParseRepoURL(in)
		if !errors.Is(err, ErrSchemelessPath) {
			t.Errorf("ParseRepoURL(%q) = %q, %v; want ErrSchemelessPath", in, got.Normalized(), err)
			continue
		}
		remedy := fileRemedy(strings.TrimSpace(in))
		if !strings.Contains(err.Error(), remedy) {
			t.Errorf("ParseRepoURL(%q) error %q does not name the remedy %q", in, err, remedy)
		}
		fixed, err := ParseRepoURL(remedy)
		if err != nil || fixed.form != formURL || fixed.u.Scheme != "file" {
			t.Errorf("remedy %q does not parse as a file URL: %+v, %v", remedy, fixed, err)
		}
		if _, err := CanonicalRepoURL(in); !errors.Is(err, ErrSyntax) {
			t.Errorf("CanonicalRepoURL(%q) err = %v; want ErrSyntax", in, err)
		}
	}
}

// A relative path is a LOCAL repository too, and the grammar has no working
// directory to resolve it against. "./bundles" would otherwise read "." as a
// host and render https://./bundles; it must be refused like an absolute one.
// Resolving it is the job of the argv ingest that has a working directory.
func TestParseRepoURL_RefusesARelativePath(t *testing.T) {
	for _, in := range []string{"./bundles.git", "./bundles/", "../bundles", "../../srv/x.git", ".", "..", "  ./x  "} {
		got, err := ParseRepoURL(in)
		if !errors.Is(err, ErrSchemelessPath) {
			t.Errorf("ParseRepoURL(%q) = %q, %v; want ErrSchemelessPath", in, got.Normalized(), err)
			continue
		}
		if _, err := CanonicalRepoURL(in); !errors.Is(err, ErrSyntax) {
			t.Errorf("CanonicalRepoURL(%q) err = %v; want ErrSyntax", in, err)
		}
	}
}

// Only a leading "./" or "../" (or a bare "." / "..") is a relative path. A
// dot elsewhere in the first segment is still a host, and a dotted repository
// name is still shorthand.
func TestParseRepoURL_DottedHostsAndNamesAreNotPaths(t *testing.T) {
	for _, in := range []string{"gitlab.com/alice/repo", "owner/repo.js", ".dotowner/repo", "..x/repo"} {
		if _, err := ParseRepoURL(in); err != nil {
			t.Errorf("ParseRepoURL(%q) = %v; want it parsed, not refused", in, err)
		}
	}
}

// A home-relative spelling is a LOCAL path too. Its first segment "~" carries
// no dot, so the shorthand arm would otherwise read "~/bundles" as the GitHub
// repository github.com/~/bundles. Expanding the home directory is the argv
// ingest's job, as resolving any path is; the grammar refuses it.
func TestParseRepoURL_RefusesAHomeRelativePath(t *testing.T) {
	for _, in := range []string{"~/bundles", "~/bundles.git", "~", "~/", "~alice/bundles", "  ~/x  "} {
		got, err := ParseRepoURL(in)
		if !errors.Is(err, ErrSchemelessPath) {
			t.Errorf("ParseRepoURL(%q) = %q, %v; want ErrSchemelessPath", in, got.Normalized(), err)
			continue
		}
		if !strings.Contains(err.Error(), homeRemedy) {
			t.Errorf("ParseRepoURL(%q) error %q does not name the remedy %q", in, err, homeRemedy)
		}
		if _, err := CanonicalRepoURL(in); !errors.Is(err, ErrSyntax) {
			t.Errorf("CanonicalRepoURL(%q) err = %v; want ErrSyntax", in, err)
		}
	}
}

// A bare word — no dot, no slash — is neither a host anyone can reach by name
// on the public network nor a repository path. Reading "bundles" as the host
// https://bundles is a guess; it is refused, naming the spellings that work.
// A dotted bare host is a real host name and stays parseable.
func TestParseRepoURL_RefusesABareWord(t *testing.T) {
	for _, in := range []string{"bundles", "localhost", "my-repo", "  x  "} {
		got, err := ParseRepoURL(in)
		if !errors.Is(err, ErrSyntax) {
			t.Errorf("ParseRepoURL(%q) = %q, %v; want ErrSyntax", in, got.Normalized(), err)
			continue
		}
		if _, err := CanonicalRepoURL(in); !errors.Is(err, ErrSyntax) {
			t.Errorf("CanonicalRepoURL(%q) err = %v; want ErrSyntax", in, err)
		}
	}
	for _, in := range []string{"gitlab.com", "example.com.git"} {
		if _, err := ParseRepoURL(in); err != nil {
			t.Errorf("ParseRepoURL(%q) = %v; a dotted bare host is not a bare word", in, err)
		}
	}
}

// The file:// remedy is a URL, and git percent-decodes a file:// URL: a path
// holding '%' concatenated after "file://" names a different directory. The
// remedy must round-trip to the path the user wrote.
func TestFileRemedy_EscapesPercent(t *testing.T) {
	const path = "/srv/100%25done/bundles"
	parsed, err := url.Parse(fileRemedy(path))
	if err != nil {
		t.Fatalf("fileRemedy(%q) = %q does not parse: %v", path, fileRemedy(path), err)
	}
	if parsed.Scheme != "file" || parsed.Path != path {
		t.Errorf("fileRemedy(%q) = %q decodes to scheme %q path %q; want file %q", path, fileRemedy(path), parsed.Scheme, parsed.Path, path)
	}
}

// TestRepoURL_Name pins the name a remote is auto-registered under for every
// address form. URL and scp forms name the final path segment; shorthand and
// host-qualified addresses keep their "owner/repo" spelling, as does every
// other form, with its separators flattened to "/"; one trailing ".git" is
// dropped unless that would empty the name.
func TestRepoURL_Name(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		want    string
	}{
		{"https", "https://github.com/owner/repo", "repo"},
		{"https trailing slash", "https://github.com/owner/repo/", "repo"},
		{"https .git", "https://github.com/owner/repo.git", "repo"},
		{"https .git before trailing slash", "https://github.com/owner/repo.git/", "repo"},
		{"http", "http://example.com/owner/my-repo", "my-repo"},
		{"http trailing slash", "http://example.com/owner/my-repo/", "my-repo"},
		{"https subgroups", "https://gitlab.com/group/subgroup/repo", "repo"},
		{"https port", "https://host:8443/owner/repo.git", "repo"},
		{"https userinfo", "https://user:pw@host/owner/repo", "repo"},
		{"https query and fragment", "https://github.com/owner/repo?x=1#frag", "repo"},
		{"https bare host", "https://github.com", ""},
		{"https bare host trailing slash", "https://github.com/", ""},
		{"https with neither host nor path is flattened", "https://", "https/"},
		{"only one .git is dropped", "https://github.com/owner/repo.git.git", "repo.git"},
		{".git inside the name is kept", "https://github.com/owner/repo.github.io", "repo.github.io"},
		{"a name that is only .git is kept rather than emptied", "https://example.com/owner/.git", ".git"},
		{"scp", "git@github.com:owner/repo", "repo"},
		{"scp .git", "git@github.com:owner/repo.git", "repo"},
		{"scp non-git user", "forge@gitlab.example.com:group/repo", "repo"},
		{"scp subgroups .git", "git@gitlab.com:group/subgroup/repo.git", "repo"},
		{"scp empty user", "@github.com:owner/repo", "repo"},
		{"scp single segment", "git@github.com:repo", "repo"},
		{"scp name that is only .git", "git@github.com:owner/.git", ".git"},
		{"ssh URL", "ssh://git@github.com/owner/repo", "repo"},
		{"ssh URL .git", "ssh://git@github.com/owner/repo.git", "repo"},
		{"ssh URL port", "ssh://git@github.com:2222/owner/repo.git", "repo"},
		{"unparseable scheme URL is flattened", "unknown://weird:format", "unknown/weird/format"},
		{"ssh URL trailing slash", "ssh://github.com/owner/repo/", "repo"},
		{"git URL", "git://example.com/owner/repo.git", "repo"},
		{"file", "file:///path/to/repo", "repo"},
		{"file trailing slash", "file:///path/to/repo/", "repo"},
		{"file .git", "file:///path/to/repo.git", "repo"},
		{"file single segment", "file:///repo", "repo"},
		{"file single segment .git", "file:///repo.git", "repo"},
		{"file local bare repository", "file:///tmp/acceptance-elsewhere/remote.git", "remote"},
		{"file with host", "file://localhost/srv/repo", "repo"},
		{"file percent-encoded", "file:///srv/my%20repo", "my repo"},
		{"shorthand", "owner/repo", "owner/repo"},
		{"shorthand .git", "owner/repo.git", "owner/repo"},
		{"shorthand trailing slash", "owner/repo/", "owner/repo/"},
		{"shorthand .git before trailing slash", "owner/repo.git/", "owner/repo.git/"},
		{"shorthand dotted repo", "owner/repo.js", "owner/repo.js"},
		{"shorthand-shaped three segments", "a/b/c", "a/b/c"},
		{"host-qualified", "github.com/owner/repo", "github.com/owner/repo"},
		{"host-qualified .git", "github.com/owner/repo.git", "github.com/owner/repo"},
		{"host-qualified trailing slash", "github.com/owner/repo/", "github.com/owner/repo/"},
		{"host-qualified subgroups", "gitlab.com/group/subgroup/repo", "gitlab.com/group/subgroup/repo"},
		{"host-qualified port", "gitlab.com:8080/group/repo", "gitlab.com/8080/group/repo"},
		{"bare host", "gitlab.com", "gitlab.com"},
		{"bare host .git", "example.com.git", "example.com"},
		{"local sentinel", "ctxloom:local", "ctxloom/local"},
		{"companion sentinel", "ctxloom:companion", "ctxloom/companion"},
		{"opaque @ form", "ctxloom:local@bundles/x", "ctxloom/local/bundles/x"},
		{"scp trailing slash", "git@github.com:owner/repo/", "repo"},
		{"scp .git before trailing slash", "git@github.com:owner/repo.git/", "repo"},
		{"surrounding whitespace", " https://github.com/owner/repo ", "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := ParseRepoURL(tt.repoURL)
			if err != nil {
				t.Fatalf("ParseRepoURL(%q): %v", tt.repoURL, err)
			}
			if got := parsed.Name(); got != tt.want {
				t.Errorf("ParseRepoURL(%q).Name() = %q, want %q", tt.repoURL, got, tt.want)
			}
		})
	}
}
