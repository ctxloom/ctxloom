package refuri

import (
	"fmt"
	"net/url"
	"strings"
)

// ParseRepoIdentity canonicalizes a repository URL into the repository half
// of a reference — Class, Host and the DECODED RepoPath, exactly the fields a
// bundle reference minted from it carries — so that every place which asks
// "is this the same repository?" answers from one value.
//
// This is the ONLY repo-level canonicalizer. It replaced two that disagreed:
// one kept host case and echoed an unparseable string back verbatim, the other
// folded host case and ALSO fell back verbatim. Two answers to "same
// repository" is a retraction recorded under one spelling and looked up under
// another, and a verbatim fallback is a key minted for a string nobody can
// address — so a string this cannot read is an ERROR, never itself.
//
// It folds what is provably the same repository and nothing else. The
// transport is not identity (scp, http, ssh:// and git:// all reach ClassGit),
// and neither are credentials, a query, a fragment, host case, a trailing
// slash, a port the scheme defines as its default, or whether an IDN host is
// spelled as its U-label or A-label. A ".git" suffix, a "www." prefix and repository-path case ARE
// identity and are preserved — see Parse's doc for why folding them on a guess
// would let a rejection of one repository govern another.
//
// The sentinel source tokens map onto their classes (ClassLocal,
// ClassCompanion), which carry no repository at all.
func ParseRepoIdentity(raw string) (Parts, error) {
	r, err := ParseRepoURL(raw)
	if err != nil {
		return Parts{}, fmt.Errorf("%w: %v", ErrSyntax, err)
	}
	switch r.form {
	case formSentinel:
		if r.kind == SourceKindCompanion {
			return Parts{Class: ClassCompanion}, nil
		}
		return Parts{Class: ClassLocal}, nil
	case formVerbatim, formOpaque:
		return Parts{}, fmt.Errorf("%w: %q is not a repository URL", ErrSyntax, r.raw)
	case formURL:
		// The WRITTEN path, because RepoPath below is the decoded one: "%2F"
		// decodes to a structural "/", so "a%2Fb" and "a/b" would become one
		// repository. Parse refuses this escape in a reference for the same
		// reason; the repository half must not accept what the reference
		// grammar refuses.
		if i := indexEncodedSlash(writtenPath(r.u)); i >= 0 {
			return Parts{}, fmt.Errorf("%w: encoded slash (%%2F) in repository path %q is not addressable", ErrSyntax, r.raw)
		}
		if r.u.Scheme == "file" {
			return repoParts(Parts{Class: ClassFile, RepoPath: r.u.Path})
		}
		host, port, err := CanonicalAuthority(r.u)
		if err != nil {
			return Parts{}, err
		}
		return repoParts(Parts{Class: ClassGit, Host: joinAuthority(host, port), RepoPath: r.u.Path})
	default:
		return repoParts(Parts{Class: ClassGit, Host: r.host, RepoPath: "/" + r.identityPath()})
	}
}

// schemeDefaultPort is the port each repository scheme DEFINES as its
// default. Only a written port equal to its own scheme's default is dropped
// (RFC 3986 §6.2.3): "https://h:22" is not "https://h", and a scheme absent
// here has no default to fold.
var schemeDefaultPort = map[string]string{
	"https":   "443",
	"http":    "80",
	"ssh":     "22",
	"git+ssh": "22",
	"ssh+git": "22",
	"git":     "9418",
}

// CanonicalAuthority is u's authority as repository identity keys it: the
// host name under CanonicalHost, and the port — "" when u writes none, when
// it is the default u's scheme defines, or when it is refHTTPSPort. That last
// fold is because transport is not identity: every ClassGit repository is
// rendered and fetched over https, so a port left in an identity IS an https
// port, and ":443" names the same server as none.
//
// Anything that decides "is this the same server" for a repository (forge
// matching included) must compare these, or one repository binds differently
// by how its URL happens to be written.
func CanonicalAuthority(u *url.URL) (host, port string, err error) {
	host, err = CanonicalHost(u.Hostname())
	if err != nil {
		return "", "", err
	}
	port = u.Port()
	if port == schemeDefaultPort[u.Scheme] || port == refHTTPSPort {
		port = ""
	}
	return host, port, nil
}

// joinAuthority renders a CanonicalAuthority pair; host is already bracketed
// when it is an IPv6 literal.
func joinAuthority(host, port string) string {
	if port == "" {
		return host
	}
	return host + ":" + port
}

// repoParts holds a repository half to the reference grammar's own rules
// (host case, dot segments, a trailing slash, refused escapes) by minting it
// under a placeholder bundle, so a repository canonicalized here and a
// reference minted over the same repository cannot disagree about it.
func repoParts(p Parts) (Parts, error) {
	// Trailing slashes first: rendered before the "//" separator, a trailing
	// "/" would read as the separator itself.
	p.RepoPath = strings.TrimRight(p.RepoPath, "/")
	// A "//" left inside the path is the repo/bundle separator, and the parse
	// inside Mint would split there: "o//bundles/r" comes back as repository
	// "o" with no error. Refused here, so it is never re-keyed as another
	// repository.
	if strings.Contains(p.RepoPath, RepoBundleSeparator) {
		return Parts{}, fmt.Errorf("%w: repository path %q contains the %q separator", ErrSyntax, p.RepoPath, RepoBundleSeparator)
	}
	p.Bundle = "_"
	minted, err := Mint(p)
	if err != nil {
		return Parts{}, err
	}
	minted.Bundle = ""
	return minted, nil
}

// CanonicalRepoURL is ParseRepoIdentity rendered by FetchURL: one spelling per
// repository, and the key every "same remote?" comparison is made on. The
// sentinel tokens render as themselves.
func CanonicalRepoURL(raw string) (string, error) {
	p, err := ParseRepoIdentity(raw)
	if err != nil {
		return "", err
	}
	return p.FetchURL(), nil
}

// FetchURL renders the repository half of p as the location its content is
// fetched from: an https URL for ClassGit, a file URL for ClassFile, the
// source token for the two internal classes, "" for the zero Parts.
//
// It is the ONE reverse renderer from a parsed reference back to a URL. The
// path is PERCENT-ENCODED, and that is the whole reason there is one: git
// decodes a file:// URL, so a decoded path concatenated after "file://" names
// a different directory the moment it holds an escape ("a%41b" reaches git as
// "aAb"), and a repository whose path carries a '%' was not addressable at all.
// https is the transport because a reference does not carry one — see Parse.
func (p Parts) FetchURL() string {
	switch p.Class {
	case ClassGit:
		return (&url.URL{Scheme: "https", Host: p.Host, Path: p.RepoPath}).String()
	case ClassFile:
		return (&url.URL{Scheme: "file", Path: p.RepoPath}).String()
	case ClassLocal:
		return LocalSource
	case ClassCompanion:
		return CompanionSource
	}
	return ""
}
