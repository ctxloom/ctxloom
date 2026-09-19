package remote

import "github.com/ctxloom/ctxloom/internal/shared/refuri"

// The repo-URL grammar, the two source tokens and the ingest normalisers are
// refuri's: declared once, below both this package and the trust tier that
// keys on them, and carried here under this package's established names for
// its callers.
type (
	RepoURL    = refuri.RepoURL
	SourceKind = refuri.SourceKind
)

const (
	SourceKindRemote    = refuri.SourceKindRemote
	SourceKindLocal     = refuri.SourceKindLocal
	SourceKindCompanion = refuri.SourceKindCompanion

	LocalSource     = refuri.LocalSource
	CompanionSource = refuri.CompanionSource
)

// ParseRepoURL is refuri.ParseRepoURL: the one repo-URL grammar.
func ParseRepoURL(raw string) (RepoURL, error) { return refuri.ParseRepoURL(raw) }

// NormalizeURL is refuri.NormalizeURL: a repository URL's IDENTITY rendering.
func NormalizeURL(repoURL string) string { return refuri.NormalizeURL(repoURL) }

// NormalizeRef is refuri.NormalizeRef: the ingest normaliser every reference
// passes through.
func NormalizeRef(ref string) string { return refuri.NormalizeRef(ref) }

// IsSelfContainedRef is refuri.IsSelfContainedRef.
func IsSelfContainedRef(ref string) bool { return refuri.IsSelfContainedRef(ref) }
