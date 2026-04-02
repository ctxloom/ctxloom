// Package remote provides functionality for managing remote fragment/prompt sources.
package remote

import (
	"time"

	"gopkg.in/yaml.v3"
)

// Remote represents a configured remote source (GitHub/GitLab repo).
type Remote struct {
	Name    string `yaml:"name" json:"name"`
	URL     string `yaml:"url" json:"url"`
	Version string `yaml:"version" json:"version"` // e.g., "v1" (version directory)
}

// SourceMeta contains provenance metadata embedded in installed fragments/prompts.
// This tracks where the item was pulled from for audit and update purposes.
type SourceMeta struct {
	Org       string    `yaml:"org" json:"org"`               // GitHub/GitLab org or user
	Name      string    `yaml:"name" json:"name"`             // Fragment/prompt name
	SHA       string    `yaml:"sha" json:"sha"`               // Full git commit SHA
	URL       string    `yaml:"url" json:"url"`               // Source repository URL
	Type      ItemType  `yaml:"type" json:"type"`             // "fragment", "prompt", or "profile"
	Version   string    `yaml:"version" json:"version"`       // Version directory (e.g., "v1")
	FetchedAt time.Time `yaml:"fetched_at" json:"fetched_at"` // When the item was pulled
}

// ItemType distinguishes between different remote item types.
type ItemType string

const (
	ItemTypeBundle  ItemType = "bundle"  // Primary content unit (contains fragments, prompts, mcp)
	ItemTypeProfile ItemType = "profile" // References bundles and their contents
)

// DirName returns the directory name for this item type in the repo structure.
func (t ItemType) DirName() string {
	switch t {
	case ItemTypeBundle:
		return "bundles"
	case ItemTypeProfile:
		return "profiles"
	default:
		return string(t) + "s"
	}
}

// Plural returns the plural form of the item type for display.
func (t ItemType) Plural() string {
	return t.DirName()
}

// SecurityWarning describes the security risks for installing content.
type SecurityWarning struct {
	Title   string
	Context string
	Risks   []string
}

// SecureContent is implemented by remote content types to provide
// security warnings and metadata for pull confirmation.
type SecureContent interface {
	SecurityWarning() SecurityWarning
	Note() string
}

// RemoteMCPServer represents an MCP server configuration from a remote source.
type RemoteMCPServer struct {
	Command           string            `yaml:"command"`
	Args              []string          `yaml:"args,omitempty"`
	Env               map[string]string `yaml:"env,omitempty"`
	NotesField        string            `yaml:"notes,omitempty"`        // Human-readable notes
	InstallationField string            `yaml:"installation,omitempty"` // Setup/installation instructions
}

func (m RemoteMCPServer) SecurityWarning() SecurityWarning {
	return SecurityWarning{
		Title:   "MCP SERVER INSTALLATION",
		Context: "You are about to install an MCP server that will execute commands on your system.",
		Risks: []string{
			"Execute arbitrary commands with your permissions",
			"Access files and environment variables",
			"Exfiltrate data from your system",
		},
	}
}

func (m RemoteMCPServer) Note() string { return m.NotesField }

// Installation returns setup/installation instructions for this MCP server.
func (m RemoteMCPServer) Installation() string { return m.InstallationField }

// RemoteContext represents a fragment, prompt, or profile from a remote source.
// These all share the same security risk (prompt injection).
type RemoteContext struct {
	NotesField        string `yaml:"notes,omitempty"`        // Human-readable notes
	InstallationField string `yaml:"installation,omitempty"` // Setup/installation instructions
}

func (c RemoteContext) SecurityWarning() SecurityWarning {
	return SecurityWarning{
		Title:   "PROMPT INJECTION RISK",
		Context: "You are about to install context that will influence AI behavior.",
		Risks: []string{
			"Override safety guidelines",
			"Exfiltrate data through crafted outputs",
			"Execute unintended actions",
		},
	}
}

func (c RemoteContext) Note() string { return c.NotesField }

// Installation returns setup/installation instructions for this context.
func (c RemoteContext) Installation() string { return c.InstallationField }

// RemoteBundle represents a bundle from a remote source.
// Bundles combine MCP servers with fragments and prompts.
type RemoteBundle struct {
	Version           string                       `yaml:"version"`
	Description       string                       `yaml:"description,omitempty"`
	NotesField        string                       `yaml:"notes,omitempty"`        // Human-readable notes
	InstallationField string                       `yaml:"installation,omitempty"` // Setup/installation instructions
	MCP               *RemoteMCPServer             `yaml:"mcp,omitempty"`
	Fragments         map[string]RemoteBundleItem  `yaml:"fragments,omitempty"`
	Prompts           map[string]RemoteBundleItem  `yaml:"prompts,omitempty"`
}

// RemoteBundleItem represents a fragment or prompt within a bundle.
type RemoteBundleItem struct {
	Tags         []string `yaml:"tags,omitempty"`
	Notes        string   `yaml:"notes,omitempty"`        // Human-readable notes
	Installation string   `yaml:"installation,omitempty"` // Setup/installation instructions
	Content      string   `yaml:"content"`
}

func (b RemoteBundle) SecurityWarning() SecurityWarning {
	risks := []string{
		"Override AI safety guidelines via bundled context",
		"Influence AI behavior through embedded prompts",
		"Exfiltrate data through crafted outputs",
	}

	title := "BUNDLE INSTALLATION"
	context := "You are about to install a bundle containing AI context."

	// Add MCP-specific risks if bundle has MCP server
	if b.MCP != nil && b.MCP.Command != "" {
		title = "BUNDLE INSTALLATION (WITH MCP SERVER)"
		context = "You are about to install a bundle with executable code AND AI context."
		risks = append([]string{
			"Execute arbitrary commands with your permissions",
			"Access files and environment variables",
		}, risks...)
	}

	return SecurityWarning{
		Title:   title,
		Context: context,
		Risks:   risks,
	}
}

func (b RemoteBundle) Note() string { return b.NotesField }

// Installation returns setup/installation instructions for this bundle.
func (b RemoteBundle) Installation() string { return b.InstallationField }

// HasMCP returns true if bundle includes an MCP server.
func (b RemoteBundle) HasMCP() bool {
	return b.MCP != nil && b.MCP.Command != ""
}

// ParseSecureContent parses raw YAML content into the appropriate SecureContent type.
func ParseSecureContent(itemType ItemType, data []byte) (SecureContent, error) {
	switch itemType {
	case ItemTypeBundle:
		var b RemoteBundle
		if err := yaml.Unmarshal(data, &b); err != nil {
			return nil, err
		}
		return b, nil
	case ItemTypeProfile:
		// Profiles use RemoteContext for security warnings
		var c RemoteContext
		if err := yaml.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		return c, nil
	default:
		var c RemoteContext
		if err := yaml.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		return c, nil
	}
}

// DirEntry represents a directory entry from a remote repository.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	SHA   string `json:"sha"`
	Size  int64  `json:"size"`
}

// RepoInfo contains metadata about a discovered remote repository.
type RepoInfo struct {
	Owner       string    `json:"owner"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Stars       int       `json:"stars"`
	URL         string    `json:"url"`
	Topics      []string  `json:"topics"`
	Language    string    `json:"language"`
	UpdatedAt   time.Time `json:"updated_at"`
	Forge       ForgeType `json:"forge"`
}

// ForgeType identifies the git hosting platform.
type ForgeType string

const (
	ForgeGitHub ForgeType = "github"
	ForgeGitLab ForgeType = "gitlab"
)

// Reference represents a parsed remote reference.
//
// Supported formats:
//
// Simple (requires remotes.yaml):
//   - alice/security → Remote="alice", Path="security"
//   - alice/security@v1.0.0 → with ContentVersion
//
// HTTPS URL (canonical, self-contained):
//   - https://github.com/owner/repo@v1/bundles/name (latest)
//   - https://github.com/owner/repo@v1/bundles/name@v1.2.3 (pinned tag)
//   - https://github.com/owner/repo@v1/bundles/name@abc123 (pinned SHA)
//
// SSH URL:
//   - git@github.com:owner/repo@v1/bundles/name@v1.2.3
//
// File URL (local repos):
//   - file:///path/to/repo@v1/bundles/name@v1.2.3
//
// Format: <repo>@<ctxloom_version>/<type>/<path>@<content_version>
// - First @ = ctxloom schema version (directory: ctxloom/v1/bundles/...)
// - Second @ = content version (git tag or SHA, optional)
type Reference struct {
	// Remote is the remote name (for simple format) or empty for URL-based refs
	Remote string

	// Path is the item name/path (e.g., "core-practices" or "golang/best-practices")
	Path string

	// URL is the full repository URL (for URL-based refs); empty for simple format
	URL string

	// Version is the ctxloom schema version directory (e.g., "v1"); extracted from URL or remote config
	Version string

	// ContentVersion is the git tag or SHA for content versioning
	// For simple refs: from @version suffix (e.g., remote/path@v1.0.0)
	// For canonical URLs: from second @ (e.g., repo@v1/bundles/name@v1.0.0)
	// Empty means use HEAD/latest
	ContentVersion string

	// ItemType is the type of item (bundles, profiles); extracted from URL path
	ItemType ItemType

	// IsCanonical indicates this is a URL-based reference (vs simple remote/path)
	IsCanonical bool
}

// LockEntry represents a locked dependency in the lockfile.
type LockEntry struct {
	// SHA is the resolved git commit SHA for reproducibility
	SHA string `yaml:"sha" json:"sha"`

	// URL is the canonical repository URL
	URL string `yaml:"url" json:"url"`

	// CtxloomVersion is the ctxloom schema version (v1, v2) - determines directory path
	CtxloomVersion string `yaml:"ctxloom_version" json:"ctxloom_version"`

	// RequestedVersion is the original tag/SHA requested by user (for export reconstruction)
	// Empty if user didn't specify a version (used HEAD)
	RequestedVersion string `yaml:"requested_version,omitempty" json:"requested_version,omitempty"`

	// FetchedAt is when the item was pulled
	FetchedAt time.Time `yaml:"fetched_at" json:"fetched_at"`
}

// Lockfile represents the .ctxloom/lock.yaml file for pinning dependencies.
type Lockfile struct {
	Version  int                  `yaml:"version" json:"version"`
	LockedAt time.Time            `yaml:"locked_at" json:"locked_at"`
	Bundles  map[string]LockEntry `yaml:"bundles,omitempty" json:"bundles,omitempty"`
	Profiles map[string]LockEntry `yaml:"profiles,omitempty" json:"profiles,omitempty"`
}

// ManifestEntry represents an item in the optional manifest.yaml index.
type ManifestEntry struct {
	Name        string   `yaml:"name" json:"name"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Author      string   `yaml:"author,omitempty" json:"author,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Version     string   `yaml:"version,omitempty" json:"version,omitempty"`
}

// Manifest represents the optional ctxloom/v1/manifest.yaml index file.
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

// RemoteConfig holds remote-related configuration.
type RemoteConfig struct {
	Remotes map[string]Remote `yaml:"remotes,omitempty" json:"remotes,omitempty"`
	Auth    AuthConfig        `yaml:"auth,omitempty" json:"auth,omitempty"`
	Replace map[string]string `yaml:"replace,omitempty" json:"replace,omitempty"` // Local overrides
	Vendor  bool              `yaml:"vendor,omitempty" json:"vendor,omitempty"`   // Use vendored deps
}

// AuthConfig holds authentication tokens for forges.
type AuthConfig struct {
	GitHub string `yaml:"github,omitempty" json:"github,omitempty"`
	GitLab string `yaml:"gitlab,omitempty" json:"gitlab,omitempty"`
}
