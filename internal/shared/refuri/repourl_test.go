package refuri

import (
	"errors"
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
