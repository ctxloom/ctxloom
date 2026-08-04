// Package remote provides functionality for managing remote fragment/prompt sources.
package remote

import (
	"time"
)

// Remote represents a configured remote source (a git repository).
type Remote struct {
	Name string `yaml:"name" json:"name"`
	URL  string `yaml:"url" json:"url"`

	// NOTE: there is deliberately no trust flag here, and there must never be
	// one again. `trust_bundles` used to mark a remote as a trusted source —
	// everything it published reached the agent unreviewed, forever, WITHOUT
	// EVER LOOKING AT THE CONTENT. That trusts a LOCATION, and a location can be
	// forked, typosquatted, or compromised; a URL you trusted once could serve
	// changed bytes silently for the rest of time.
	//
	// Trust is now keyed to the signing IDENTITY (a publisher key in
	// allowed_signers), verified over the bytes themselves at the exposure gate.
	// A remote is just an address to fetch from, and carries no authority at all.

	// Forge is the label of the forges entry this remote binds to. Empty
	// means resolve by URL host (configured base_url match, else built-in
	// github for github.com, else generic git).
	Forge string `yaml:"forge,omitempty" json:"forge,omitempty"`
}

// ItemType distinguishes between different remote item types.
type ItemType string

const (
	// ItemTypeBundle is the only distributed item type. Top-level profile
	// distribution was retired; profiles ship inside bundles (<bundle>#profiles/).
	ItemTypeBundle ItemType = "bundle" // Primary content unit (fragments, commands, mcp, hooks, profiles)
)

// DirName returns the directory name for this item type in the repo structure.
func (t ItemType) DirName() string {
	switch t {
	case ItemTypeBundle:
		return "bundles"
	default:
		return string(t) + "s"
	}
}

// DirEntry represents a directory entry from a remote repository.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	// Executable reports that the entry is a file the publisher committed as
	// mode 100755. It is the ONLY mode bit git records for a blob, so it is the
	// only one a listing can honestly carry — and it is carried because a
	// bundle tree ships skill scripts the model runs itself, which arrive
	// useless if the bit is dropped in transit. Always false for directories,
	// and for any forge whose listing does not report file modes (see
	// GitHubFetcher.ListDir): a fetcher that cannot tell must say "not
	// executable" rather than guess a bit on.
	Executable bool `json:"executable,omitempty"`
}

// RepoInfo contains metadata about a discovered remote repository.
type RepoInfo struct {
	Owner       string    `json:"owner"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Stars       int       `json:"stars"`
	URL         string    `json:"url"`
	Forge       ForgeType `json:"forge"`
}

// ForgeType identifies the git hosting platform.
type ForgeType string

const (
	// ForgeGitHub is the rich GitHub adapter (REST API reads, search, publish).
	ForgeGitHub ForgeType = "github"
	// ForgeGitGeneric is the generic git adapter: clone over the system git
	// path and read locally. Serves any other host (GitLab, Gitea, Bitbucket,
	// self-hosted) for consumption; no API search or publish.
	ForgeGitGeneric ForgeType = "git"
)

// Reference represents a parsed remote reference.
//
// Supported formats (all scheme-qualified — the short "repo/path" form is gone):
//
// HTTPS URL (canonical, self-contained):
//   - https://github.com/owner/repo@bundles/name (latest)
//   - https://github.com/owner/repo@bundles/name@v1.2.3 (pinned tag)
//   - https://github.com/owner/repo@bundles/name@abc123 (pinned SHA)
//
// SSH URL:
//   - git@github.com:owner/repo@bundles/name@v1.2.3
//
// File URL (local repos):
//   - file:///path/to/repo@bundles/name@v1.2.3
//
// Local source:
//   - ctxloom:local@bundles/name[@rev]
//
// Format: <repo>@<type>/<path>@<content_version>
// - First @ = item type and path (bundles/<name> or profiles/<name>)
// - Second @ = content version (git tag or SHA, optional)
// Pre-removal refs carried a schema-version segment (repo@v1/<type>/<path>);
// it is silently dropped on parse (see stripLegacySchemaSegment).
type Reference struct {
	// Path is the item name/path (e.g., "core-practices" or "golang/best-practices")
	Path string

	// URL is the full repository URL (for URL-based refs); empty for local refs
	URL string

	// ContentVersion is the git tag or SHA for content versioning
	// For simple refs: from @version suffix (e.g., remote/path@v1.0.0)
	// For canonical URLs: from second @ (e.g., repo@bundles/name@v1.0.0)
	// Empty means use HEAD/latest
	ContentVersion string

	// ItemType is the type of item (bundles, profiles); extracted from URL path
	ItemType ItemType

	// IsLocal indicates a ctxloom:local reference — project-authored content
	// read from the committed .ctxloom/content/ working copy rather than a remote
	// clone. Local refs share the canonical grammar tail (@kind/name[@version])
	// but their source is the fixed "ctxloom:local" token, so they carry no URL.
	// ContentVersion is usually empty (the version is the surrounding project's
	// own VCS state); a non-empty value pins to a project revision.
	IsLocal bool

	// IsCompanion indicates a ctxloom:companion@<bin> reference — a bundle
	// EMITTED LIVE by a companion binary on PATH (`<bin> loadout --format
	// json`, signature-envelope spec §4.3), rather than read from a git tree.
	// Deliberately a SEPARATE flag from IsLocal: a companion loadout is
	// third-party content arriving from elsewhere (the binary's author, not
	// this project), so it must NOT auto-allow the way local content does — it
	// is judged by who signed it (trusted-signer) or sent to review (pending),
	// exactly like a remote bundle. See trust.ParseItemRef, which
	// relies on IsLocal staying false here to route it through the gate
	// instead of the local exemption.
	IsCompanion bool
}

// LockEntry represents a locked dependency in the lockfile.
type LockEntry struct {
	// SHA is the resolved git commit SHA for reproducibility
	SHA string `yaml:"sha" json:"sha"`

	// URL is the canonical repository URL
	URL string `yaml:"url" json:"url"`

	// RequestedVersion is the version constraint the manifest asked for — a
	// semver range, branch, tag, SHA, or empty (track default branch). It is the
	// authored intent; SHA is what that intent resolved to. A relock carries the
	// SHA forward unchanged while RequestedVersion is unchanged (npm-style
	// stability); `upgrade` re-resolves it.
	RequestedVersion string `yaml:"requested_version,omitempty" json:"requested_version,omitempty"`

	// Version is the concrete tag a version/tag selector resolved to (e.g.
	// "v1.3.0"), recorded for display and to test whether a carried-forward SHA
	// still satisfies a changed constraint. Empty for branch and bare-SHA
	// resolutions, which have no single tag label.
	Version string `yaml:"version,omitempty" json:"version,omitempty"`

	// Kind is the classified selector kind (sha/tag/version/branch) of
	// RequestedVersion, recorded so update/upgrade semantics are declarative: a
	// sha/tag is a pin (never outdated), a version re-resolves within its range, a
	// branch re-resolves to its tip. Empty on entries written before this field
	// existed — SelectorKind() derives it from RequestedVersion in that case.
	Kind SelectorKind `yaml:"kind,omitempty" json:"kind,omitempty"`

	// FetchedAt is when the item was pulled
	FetchedAt time.Time `yaml:"fetched_at" json:"fetched_at"`

	// Pinned freezes this entry at the recorded SHA (a "hold"): `remote upgrade`
	// leaves it put even when its constraint would allow a newer commit, and a
	// lock rebuild carries the flag forward. Toggled via `ctxloom bundle
	// hold`/`unhold`.
	Pinned bool `yaml:"pinned,omitempty" json:"pinned,omitempty"`

	// Retracted records that the publisher withdrew this bundle (or the exact
	// version pinned here) — learned from the remote manifest at the last
	// sync/pull that had the network in hand (internal/remote/retract.go
	// CheckRetracted), never derived locally. operations.EffectiveTrust reads
	// this field (via its RetractionRecords seam) to withhold exposure WITHOUT
	// making a network call at exposure time. A lock rebuild (operations.
	// LockDependencies) carries it forward from the previous lockfile the same
	// way it carries Pinned forward — a full relock must not silently
	// un-retract something no fresh check has actually cleared.
	Retracted bool `yaml:"retracted,omitempty" json:"retracted,omitempty"`

	// RetractedReason is the publisher's stated reason for the retraction
	// (display-only — never a decision input). Empty when Retracted is false.
	RetractedReason string `yaml:"retracted_reason,omitempty" json:"retracted_reason,omitempty"`

	// RetractionCheckedAt is when Retracted/RetractedReason were last
	// established by an ACTUAL manifest read (Puller.resolveRetraction's
	// "Fresh" branch) — never bumped by a fallback that reused a previously
	// recorded verdict because the remote could not be reached. This is what
	// lets a later fallback (see RetractionStaleAfter) know how old the
	// verdict it is honoring actually is. Zero on an entry written before this
	// field existed, OR on an entry that has never had a manifest
	// successfully read for it — both read as UNKNOWN AGE, which
	// resolveRetraction always warns about when it falls back to them; zero is
	// deliberately not treated as "just checked" (that would silently read as
	// fresher than it is) nor as "definitely stale" (a fresh check with no
	// prior fallback has nothing to be stale relative to).
	RetractionCheckedAt time.Time `yaml:"retraction_checked_at,omitempty" json:"retraction_checked_at,omitempty"`

	// Tree records that this bundle was published in DIRECTORY form and
	// installed as a tree under the cache (Reference.LocalTreePath), rather than
	// fetched as a single "<name>.yaml" out of the clone on demand.
	//
	// It is recorded rather than re-derived because the two forms are read
	// through completely different machinery, and a reader that guessed wrong
	// fails in the least useful way available: BundleReader would go looking for
	// a "<name>.yaml" that was never published and report "remote content not
	// found" against a bundle that pulled perfectly, sending the user to re-run
	// the pull that already worked. The pin knows which shape it installed, so
	// the pin says so.
	Tree bool `yaml:"tree,omitempty" json:"tree,omitempty"`
}

// Lockfile represents the .ctxloom/lock.yaml file for pinning dependencies.
// Only bundles are locked (top-level profile distribution was retired).
type Lockfile struct {
	Version  int                  `yaml:"version" json:"version"`
	LockedAt time.Time            `yaml:"locked_at" json:"locked_at"`
	Bundles  map[string]LockEntry `yaml:"bundles,omitempty" json:"bundles,omitempty"`
}

// ManifestEntry represents an item in the optional manifest.yaml index.
type ManifestEntry struct {
	Name        string   `yaml:"name" json:"name"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Author      string   `yaml:"author,omitempty" json:"author,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Version     string   `yaml:"version,omitempty" json:"version,omitempty"`
}

// Manifest represents the optional ctxloom/manifest.yaml index file.
type Manifest struct {
	Version     int             `yaml:"version" json:"version"`
	GeneratedAt time.Time       `yaml:"generated_at" json:"generated_at"`
	Bundles     []ManifestEntry `yaml:"bundles,omitempty" json:"bundles,omitempty"`
	Profiles    []ManifestEntry `yaml:"profiles,omitempty" json:"profiles,omitempty"`
	Retracted   []RetractEntry  `yaml:"retracted,omitempty" json:"retracted,omitempty"`
}

// RetractEntry marks a bad version that should not be used.
type RetractEntry struct {
	Type    ItemType `yaml:"type" json:"type"`
	Name    string   `yaml:"name" json:"name"`
	Version string   `yaml:"version" json:"version"`
	Reason  string   `yaml:"reason" json:"reason"`
}

// SearchQuery represents a parsed search query with filters.
type SearchQuery struct {
	Text    string   // Full-text search term
	Tags    TagQuery // Tag filter expression
	Author  string   // Author filter
	Version string   // Version constraint
}

// TagQuery represents a tag filter with boolean operators.
// Uses postfix notation: foo/bar/AND, foo/bar/OR, foo/NOT
type TagQuery struct {
	Tags     []string
	Operator TagOperator
	Negated  bool
}

// TagOperator defines boolean operators for tag queries.
type TagOperator string

const (
	TagOperatorAND TagOperator = "AND"
	TagOperatorOR  TagOperator = "OR"
)

// AuthConfig holds authentication tokens for forges.
type AuthConfig struct {
	GitHub string `yaml:"github,omitempty" json:"github,omitempty"`
}
