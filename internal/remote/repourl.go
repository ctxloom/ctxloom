package remote

import (
	"fmt"
	"net/url"
	"strings"
)

// This file is the ONE place the repo-URL grammar lives.
//
// It replaced three independent re-implementations that had drifted apart:
// NormalizeURL (identity), normalizeCloneURL (transport) and
// RepoDirForURL's own url.Parse (filesystem). The four concerns below are
// legitimately distinct — a trust key is not a clone argument is not a cache
// path — but they were each re-deriving "what shape is this string?", and the
// derivations disagreed. Two shorthand arms with different guards is a bug you
// cannot see in either file alone.
//
// Parse once, render per concern:
//
//	ParseRepoURL(raw) (RepoURL, error)   the grammar
//	  .Normalized()  string              identity  -> trust namespace, lockfile keys, remotes.yaml
//	  .CloneArg()    string              transport -> what git receives
//	  .CacheSegments() ([]string, error)  filesystem -> cache path segments
//	  .Kind()        SourceKind          dispatch  -> local / companion / remote
//
// Adding a fifth consumer means adding a fifth renderer here, not a fourth
// copy of the grammar somewhere else.

// SourceKind classifies what a parsed repo URL addresses. It is the dispatch
// concern: callers that must treat project-local or companion content
// differently from a real remote branch on this rather than re-testing the
// sentinel tokens themselves.
type SourceKind string

const (
	// SourceKindRemote is a real repository reachable by git.
	SourceKindRemote SourceKind = "remote"
	// SourceKindLocal is the ctxloom:local sentinel — project-authored
	// content under the committed .ctxloom/content/ working copy.
	SourceKindLocal SourceKind = "local"
	// SourceKindCompanion is the ctxloom:companion sentinel — a loadout
	// emitted live by a companion binary on PATH.
	SourceKindCompanion SourceKind = "companion"
)

// repoURLForm is the shape ParseRepoURL recognised. It exists so the shorthand
// decision is made EXACTLY ONCE, at parse time: the two former normalizers had
// the same shorthand arm guarded differently, and only one of them had been
// fixed when the guard was found wrong. With the classification here, "two
// disagreeing shorthand arms" is not a thing that can be written.
type repoURLForm int

const (
	// formURL: an explicit scheme, "https://github.com/owner/repo".
	formURL repoURLForm = iota
	// formSCP: git's scp-like syntax, "git@github.com:owner/repo".
	formSCP
	// formShorthand: "owner/repo" — GitHub shorthand.
	formShorthand
	// formHostPath: "gitlab.com/alice/repo" — a host-qualified path that
	// merely omitted its scheme. See shorthandFirstSegment.
	formHostPath
	// formBareHost: "gitlab.com" — a host with no path at all.
	formBareHost
	// formSentinel: the ctxloom:local / ctxloom:companion tokens, which are
	// not URLs and must never be rewritten as though they were.
	formSentinel
	// formOpaque: anything else (notably a string carrying an "@" that is
	// neither scp-like nor a sentinel). Rendered as an https host, which is
	// what the previous implementation's final fallback did.
	formOpaque
	// formVerbatim: a string that CLAIMS a scheme but does not parse as a URL
	// or carries neither host nor path ("https://"). It is echoed back
	// unchanged: inventing structure for it would turn a degenerate input into
	// a plausible-looking URL, and every consumer of that answer is one that
	// clones, deletes or trust-keys whatever it is handed.
	formVerbatim
)

// RepoURL is a parsed repository URL. Construct it with ParseRepoURL; the
// zero value is not meaningful.
type RepoURL struct {
	kind SourceKind
	form repoURLForm

	// u is the parsed URL for formURL, retained whole so query, fragment,
	// userinfo and port survive a round trip. Only Path is ever rewritten.
	u *url.URL

	user string // scp user ("git" in git@host:path)
	host string // host[:port]; empty for file:/// paths
	path string // no leading or trailing "/", no trailing ".git"

	// pathVerbatim is the path exactly as written, minus a leading "/", and
	// is what the URL renderers emit for a NON-http(s) scheme.
	//
	// This is not fastidiousness, it is the one place this refactor could
	// have failed OPEN. Folding "file:///srv/repo/" onto "file:///srv/repo"
	// would move that repository's trust-namespace key, and unlike the
	// scheme-less spellings this change deliberately re-keys, a file://,
	// ssh:// or git:// URL names a repository that REALLY EXISTS — so a
	// REJECTION recorded under the old spelling could exist, and a moved key
	// is a store miss, which drops the rejection and lets EffectiveTrust
	// step 5 ALLOW the item on its publisher signature. Approvals breaking is
	// cheap (the item returns to pending); rejections breaking is not.
	//
	// trust.CanonicalRepoURL already declines to fold anything on these
	// transports for the neighbouring reason ("their path is verbatim, where
	// case may be significant"). This keeps the two layers agreeing.
	pathVerbatim string

	// gitSuffix records that the raw input carried a trailing ".git". It is
	// a property of the INPUT, not of any rendering: identity and transport
	// disagree about whether to keep it, and both need to know it was there.
	gitSuffix bool

	raw string
}

// shorthandFirstSegment reports whether a scheme-less, "@"-free token that
// contains a "/" is GitHub shorthand ("owner/repo") rather than a
// host-qualified path that merely omitted its scheme ("gitlab.com/alice/repo").
//
// THIS IS THE ARM THAT DRIFTED. NormalizeURL had no guard at all and sent
// "gitlab.com/alice/repo" to "https://github.com/gitlab.com/alice/repo";
// normalizeCloneURL required no "." ANYWHERE in the token, which fixed that
// case but broke "owner/repo.js" (returned bare, so `git clone` read it as a
// local filesystem path). Neither was right.
//
// The rule is: a GitHub OWNER name cannot contain a dot, so a dot in the FIRST
// segment means the first segment is a hostname. Testing the first segment
// rather than the whole token keeps "owner/repo.js" and "owner/repo.git"
// shorthand — the latter being why the ".git" suffix is
// stripped before this is consulted.
func shorthandFirstSegment(token string) bool {
	first, _, ok := strings.Cut(token, "/")
	return ok && first != "" && !strings.Contains(first, ".")
}

// ParseRepoURL parses a repository URL into the one representation every
// consumer renders from. It errors only on empty input; every other string is
// classified into some form, because the callers it replaces were all total
// functions over strings arriving from argv, remotes.yaml and lockfiles.
func ParseRepoURL(raw string) (RepoURL, error) {
	// Ingest boundary: a repo URL reaching here came from argv, remotes.yaml
	// or a lockfile. Its normalised form becomes the trust key and the left
	// half of the countersign ref, so it is held to the same
	// no-control-characters rule as any other reference — and doing it HERE
	// rather than in one renderer is why the clone argument now gets the same
	// guarantee the trust key always had.
	raw = NormalizeRef(strings.TrimSpace(raw))
	if raw == "" {
		return RepoURL{}, fmt.Errorf("empty repository URL")
	}

	// Sentinels first: they contain a ":" and would otherwise be mangled into
	// "https://ctxloom:local". trust.CanonicalRepoURL carried explicit early
	// returns for exactly that reason; recognising them in the grammar makes
	// those guards belt-and-braces rather than load-bearing.
	switch raw {
	case LocalSource:
		return RepoURL{kind: SourceKindLocal, form: formSentinel, raw: raw}, nil
	case CompanionSource:
		return RepoURL{kind: SourceKindCompanion, form: formSentinel, raw: raw}, nil
	}

	r := RepoURL{kind: SourceKindRemote, raw: raw}

	switch {
	case strings.Contains(raw, "://"):
		r.form = formURL
		u, err := url.Parse(raw)
		if err != nil || (u.Host == "" && u.Path == "") {
			// Unparseable or degenerate ("https://"): keep the raw string and
			// render it verbatim rather than inventing structure for it.
			r.form = formVerbatim
			return r, nil
		}
		r.u = u
		r.host = u.Host
		r.pathVerbatim = strings.TrimPrefix(u.Path, "/")
		r.path, r.gitSuffix = trimPathSuffixes(u.Path)
		return r, nil

	case strings.HasPrefix(raw, "git@"):
		user, rest, _ := strings.Cut(raw, "@")
		host, path, ok := strings.Cut(rest, ":")
		if !ok || host == "" || path == "" {
			r.form = formOpaque
			r.path, r.gitSuffix = strings.TrimSuffix(raw, ".git"), strings.HasSuffix(raw, ".git")
			return r, nil
		}
		r.form, r.user, r.host = formSCP, user, host
		r.path, r.gitSuffix = trimPathSuffixes(path)
		return r, nil

	case strings.Contains(raw, "@"):
		// An "@" that is neither scp-like nor a sentinel. The old shorthand
		// arm excluded these explicitly and so does this one: "ctxloom:local@
		// bundles/x" must not become "https://github.com/ctxloom:local@bundles/x".
		r.form = formOpaque
		r.path, r.gitSuffix = strings.TrimSuffix(raw, ".git"), strings.HasSuffix(raw, ".git")
		return r, nil

	case strings.Contains(raw, "/"):
		path, hadGit := trimPathSuffixes(raw)
		r.gitSuffix = hadGit
		if shorthandFirstSegment(path) {
			r.form, r.host, r.path = formShorthand, "github.com", path
		} else {
			host, rest, _ := strings.Cut(path, "/")
			r.form, r.host, r.path = formHostPath, host, rest
		}
		return r, nil

	default:
		// A bare host has no path, so a trailing ".git" here is part of the
		// HOST NAME — "example.com.git" is a different DNS name from
		// "example.com", not the same repository spelled twice. Trimming it
		// would invent a host the user never named, so this arm keeps the
		// slashes trimmed and the suffix intact. gitSuffix stays false: it
		// records a repository-path suffix, and there is no path here.
		r.form, r.host = formBareHost, strings.Trim(raw, "/")
		return r, nil
	}
}

// trimPathSuffixes strips surrounding slashes and a trailing ".git", in the
// order trailing-slash → ".git" → trailing-slash, and reports whether a ".git"
// was present.
//
// The ORDER is an earlier per-function fix, generalised. normalizeCloneURL
// trimmed ".git" first so that "owner/repo.git" could still be recognised as
// shorthand;
// NormalizeURL trimmed it per-arm and so left ".git" on "…/repo.git/", where
// the trailing slash hid it — connascence of order that trust.CanonicalRepoURL
// had to compensate for from another package. Doing all three here, once,
// makes the function total over the suffix spellings and lets every renderer
// decide only whether it WANTS the ".git" back.
func trimPathSuffixes(p string) (path string, hadGit bool) {
	p = strings.Trim(p, "/")
	if strings.HasSuffix(p, ".git") {
		p, hadGit = strings.TrimSuffix(p, ".git"), true
		p = strings.TrimRight(p, "/")
	}
	return p, hadGit
}

// Kind reports whether this addresses a real remote, project-local content, or
// a companion loadout.
func (r RepoURL) Kind() SourceKind { return r.kind }

// Normalized renders the IDENTITY of the repository: the string that keys the
// trust namespace (via trust.CanonicalRepoURL), names a remote in remotes.yaml,
// and appears in lockfile keys.
//
// TRANSPORT IS NOT IDENTITY, and that is a decision, not an accident of this
// function. Every transport spelling of one repository collapses here: scp,
// shorthand, scheme-less host paths and http all render as https. Which
// transport reached a repository is a statement about the caller's
// CREDENTIALS — an ssh key versus a token — and a user who switches remotes
// from git@ to https has not changed which repository they are addressing, so
// they must not lose their approvals or escape their rejections by doing it.
// Do not add a transport dimension here or to trust.BundleRef's ClassGit,
// which abstracts the same thing for the same reason.
//
// EVERY OTHER SPELLING IS PRESERVED byte-exact, including a ".git" suffix, on
// every scheme. The two rules are one rule applied twice: fold what is
// provably the same repository, preserve what is only PROBABLY the same.
// Whether "host/foo.git" and "host/foo" are one repository is host-specific
// knowledge this layer does not have — on a plain git server serving a bare
// repo, "host/foo.git" is the real path and "host/foo" may not exist at all —
// so folding them would merge two identities onto one trust key, letting a
// rejection of one silently govern the other. See trust.ParseBundleRef's doc
// for the same rule stated at the grammar, and docs/trust-model.md for what
// the narrower ref-reject scope means for the threat model.
func (r RepoURL) Normalized() string { return r.render() }

// render is the ONE place a RepoURL becomes a string. Normalized and CloneArg
// both go through it and both get the SAME path spelling — there is no
// per-caller variant, because two renderers that can disagree about what
// repository a string names is the exact defect the shared parse was written
// to end (see TestRepoURL_IdentityAndTransportAgree).
//
// It collapses transport by construction: scp, shorthand, scheme-less host
// paths and http all render as https, because which transport reached a
// repository is a statement about the caller's CREDENTIALS and not about
// which repository it is. There is deliberately no arm that can reintroduce
// it, and CloneArg's scp case is the one deliberate divergence — it keeps the
// scp form so a clone does not lose the user's ssh key.
func (r RepoURL) render() string {
	p := r.identityPath()
	switch r.form {
	case formSentinel, formVerbatim:
		return r.raw
	case formURL:
		return r.renderURL(p)
	case formSCP, formShorthand, formHostPath:
		return "https://" + r.host + "/" + p
	case formBareHost:
		return "https://" + r.host
	default: // formOpaque
		return "https://" + p
	}
}

// identityPath is the path as a REFERENCE spells it: r.path with the ".git"
// suffix restored when the input carried one. r.path stays suffix-free for the
// consumers that genuinely need it stripped — CacheSegments, so two spellings
// of one repo share one clone directory, and the forge API callers. See
// Normalized's doc for why the suffix is not folded off an identity.
func (r RepoURL) identityPath() string {
	if r.gitSuffix {
		return r.path + ".git"
	}
	return r.path
}

// CloneArg renders the TRANSPORT form: the argument handed to `git clone` and
// `git fetch`.
//
// It differs from Normalized in exactly two places, both deliberate:
//
//   - the scp-like form is preserved rather than rewritten to https, because
//     git clones it natively and rewriting it would discard the user's chosen
//     transport (and their ssh key with it);
//   - the ".git" suffix survives on the scp form for the same reason it
//     survives on file:// and ssh:// in Normalized — it is part of a path on
//     the far side, not a forge cosmetic.
//
// Everything else — the shorthand expansion above all — is IDENTICAL to
// Normalized, and that is the point of the file. The previous split let
// "owner/repo.js" clone as a local directory and "gitlab.com/alice/repo"
// resolve to two different repositories depending on which helper you asked.
func (r RepoURL) CloneArg() string {
	if r.form == formSCP {
		return r.user + "@" + r.host + ":" + r.identityPath()
	}
	return r.render()
}

// CacheSegments returns the path segments naming this repo's clone directory
// under the cache root, host first. It is the FILESYSTEM concern: the host is
// lowercased (a mixed-case host must not key a second, distinct clone — and
// the auth header is keyed on the lowercased host) and the ".git"
// suffix never appears, so the two spellings of one repo share one clone.
//
// It returns an error for a sentinel or a degenerate URL that yields no
// segments: the returned directory is later RemoveAll'd, so "resolves to the
// cache root itself" is exactly as dangerous as "escapes the cache root"
// and must never be answered with a silent fallback.
func (r RepoURL) CacheSegments() ([]string, error) {
	if r.form == formSentinel || r.form == formVerbatim || r.form == formOpaque {
		return nil, fmt.Errorf("%q is not a clonable repository URL", r.raw)
	}
	host := r.host
	if r.form == formURL && r.u != nil {
		host = r.u.Hostname() // drops the port: one repo, one clone dir
	} else if h, _, found := strings.Cut(host, ":"); found {
		host = h
	}
	segs := splitPathSegments(strings.ToLower(host))
	segs = append(segs, splitPathSegments(r.path)...)
	if len(segs) == 0 {
		return nil, fmt.Errorf("repo URL %q resolves to no usable path segments (would be the cache root itself)", r.raw)
	}
	return segs, nil
}

// OwnerRepo renders the API-PATH concern: the first two path segments, which
// a forge REST path is built from ("/repos/<owner>/<repo>/…").
//
// It errors rather than guessing when the URL names fewer than two segments —
// callers interpolate the result into a request path, and an empty segment
// there is a request to the wrong endpoint, not a smaller one.
//
// GitHub shorthand is held to EXACTLY two segments, preserving the stricter
// guard the old standalone implementation applied to that form alone: "a/b/c"
// is not an owner/repo pair, and silently reading it as ("a","b") would build
// a plausible request for a repository the user did not name.
func (r RepoURL) OwnerRepo() (owner, repo string, err error) {
	if r.form == formSentinel || r.form == formVerbatim {
		return "", "", fmt.Errorf("%q names no owner/repo pair", r.raw)
	}
	segs := strings.Split(r.path, "/")
	if r.form == formShorthand && len(segs) != 2 {
		return "", "", fmt.Errorf("invalid shorthand format, expected 'owner/repo': %s", r.raw)
	}
	if len(segs) < 2 || segs[0] == "" || segs[1] == "" {
		return "", "", fmt.Errorf("URL path must contain owner/repo: %s", r.raw)
	}
	return segs[0], segs[1], nil
}

// splitPathSegments splits on "/" and drops empty, "." and ".." segments so a
// crafted URL cannot traverse out of the cache root.
func splitPathSegments(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// isHTTP reports whether this is an http(s) URL, the only transport whose
// ".git" suffix is cosmetic.
func (r RepoURL) isHTTP() bool {
	if r.form != formURL || r.u == nil {
		return false
	}
	return r.u.Scheme == "http" || r.u.Scheme == "https"
}

// renderURL rebuilds an explicit-scheme URL from the parsed value. Query,
// fragment, userinfo and port survive verbatim: they are not part of a repo's
// identity, but stripping them is trust.CanonicalRepoURL's job, not this
// layer's — a clone argument may legitimately need them.
//
// Only http(s) is rewritten at all. On http(s) the trailing ".git" is a forge
// cosmetic and a trailing slash is noise, and trust.CanonicalRepoURL already
// removes both, so folding them here moves no key. On every other scheme the
// httpPath is the path spelling render resolved for an http(s) URL. A
// non-http(s) path is emitted byte-for-byte as written — see pathVerbatim for why that is
// load-bearing rather than timid.
func (r RepoURL) renderURL(httpPath string) string {
	v := *r.u
	p := r.pathVerbatim
	if r.isHTTP() {
		p = httpPath
	}
	if p != "" {
		p = "/" + p
	}
	if p == "" && v.Host == "" {
		p = "/"
	}
	v.Path = p
	v.RawPath = ""
	return v.String()
}
