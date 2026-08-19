package remote

import (
	"strings"
	"testing"
)

// The short "repo/path" form has been eliminated: ParseReference rejects any
// scheme-less reference, naming the canonical/local forms to use instead.
func TestParseReference_ShortFormRejected(t *testing.T) {
	for _, input := range []string{
		"alice/security",
		"alice/security@v1.0.0",
		"alice/golang/best-practices",
		"corp/lang/go/testing/mocks@main",
		"alice",     // no slash
		"/security", // empty remote
		"alice/",    // empty path
		"",          // empty
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseReference(input); err == nil {
				t.Errorf("ParseReference(%q) expected an error (short form is gone), got nil", input)
			}
		})
	}
}

func TestParseReference_HTTPS(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantURL  string
		wantType ItemType
		wantPath string
		wantErr  bool
	}{
		{
			name:     "github bundle",
			input:    "https://github.com/owner/repo@bundles/core-practices",
			wantURL:  "https://github.com/owner/repo",
			wantType: ItemTypeBundle,
			wantPath: "core-practices",
		},
		{
			name:     "nested path",
			input:    "https://github.com/ctxloom/ctxloom-github@bundles/golang/testing",
			wantURL:  "https://github.com/ctxloom/ctxloom-github",
			wantType: ItemTypeBundle,
			wantPath: "golang/testing",
		},
		{
			name:    "fragments no longer supported",
			input:   "https://gitlab.com/group/project@fragments/security",
			wantErr: true,
		},
		{
			name:    "prompts no longer supported",
			input:   "https://github.com/owner/repo@prompts/code-review",
			wantErr: true,
		},
		{
			name:    "mcp-servers no longer supported",
			input:   "https://github.com/owner/repo@mcp-servers/sequential-thinking",
			wantErr: true,
		},
		{
			name:    "missing version",
			input:   "https://github.com/owner/repo/bundles/core",
			wantErr: true,
		},
		{
			name:    "missing type",
			input:   "https://github.com/owner/repo@core",
			wantErr: true,
		},
		{
			name:    "invalid type",
			input:   "https://github.com/owner/repo@invalid/core",
			wantErr: true,
		},
		{
			name:     "legacy v1 schema segment stripped (bundle)",
			input:    "https://github.com/owner/repo@v1/bundles/core-practices",
			wantURL:  "https://github.com/owner/repo",
			wantType: ItemTypeBundle,
			wantPath: "core-practices",
		},
		{
			// A bundle ref carrying a fragment selector identifies the bundle,
			// not the fragment — the selector must not leak into Path (and thus
			// the canonical ref / lockfile key).
			name:     "fragment selector stripped from bundle path",
			input:    "https://github.com/ctxloom/ctxloom-default@bundles/code-review-base#fragments/synthesis",
			wantURL:  "https://github.com/ctxloom/ctxloom-default",
			wantType: ItemTypeBundle,
			wantPath: "code-review-base",
		},
		{
			name:     "fragment selector stripped, version preserved",
			input:    "https://github.com/ctxloom/ctxloom-default@bundles/code-review-base#fragments/golang@abc123",
			wantURL:  "https://github.com/ctxloom/ctxloom-default",
			wantType: ItemTypeBundle,
			wantPath: "code-review-base",
		},
		{
			// The item-path separator must be found AFTER the
			// authority, not at the FIRST "@" in the whole string — a URL
			// carrying userinfo has an earlier "@" that is part of the host,
			// not the item-path separator. Before the fix this mis-split into
			// URL="https://user" and misread "host/repo" as an unknown item
			// type.
			name:     "userinfo in authority does not confuse the item-path separator",
			input:    "https://user@host/repo@bundles/name",
			wantURL:  "https://user@host/repo",
			wantType: ItemTypeBundle,
			wantPath: "name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReference(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseReference(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseReference(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", got.URL, tt.wantURL)
			}
			if got.ItemType != tt.wantType {
				t.Errorf("ItemType = %q, want %q", got.ItemType, tt.wantType)
			}
			if got.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", got.Path, tt.wantPath)
			}
			if !got.IsCanonical() {
				t.Errorf("IsCanonical = false, want true for URL reference")
			}
		})
	}
}

func TestStripLegacySchemaSegment(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"v1 before bundles", "v1/bundles/core", "bundles/core"},
		{"v1 keeps content version", "v1/bundles/core@v1.2.3", "bundles/core@v1.2.3"},
		{"already clean type/path untouched", "bundles/core", "bundles/core"},
		{"unknown leading without type follows is untouched", "invalid/core", "invalid/core"},
		{"single segment untouched", "core", "core"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripLegacySchemaSegment(tt.in); got != tt.want {
				t.Errorf("stripLegacySchemaSegment(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseReference_SSH(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantURL  string
		wantType ItemType
		wantPath string
		wantErr  bool
	}{
		{
			name:     "github ssh bundle",
			input:    "git@github.com:owner/repo@bundles/core-practices",
			wantURL:  "git@github.com:owner/repo",
			wantType: ItemTypeBundle,
			wantPath: "core-practices",
		},
		{
			name:    "fragments no longer supported",
			input:   "git@gitlab.com:group/subgroup/repo@fragments/security",
			wantErr: true,
		},
		{
			name:    "missing version",
			input:   "git@github.com:owner/repo/bundles/core",
			wantErr: true,
		},
		{
			name:    "missing colon",
			input:   "git@github.com/owner/repo@bundles/core",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReference(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseReference(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseReference(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", got.URL, tt.wantURL)
			}
			if got.ItemType != tt.wantType {
				t.Errorf("ItemType = %q, want %q", got.ItemType, tt.wantType)
			}
			if got.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", got.Path, tt.wantPath)
			}
			if !got.IsCanonical() {
				t.Errorf("IsCanonical = false, want true for SSH reference")
			}
		})
	}
}

func TestParseReference_File(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantURL  string
		wantType ItemType
		wantPath string
		wantErr  bool
	}{
		{
			name:     "absolute path bundle",
			input:    "file:///home/user/ctxloom-content@bundles/core-practices",
			wantURL:  "file:///home/user/ctxloom-content",
			wantType: ItemTypeBundle,
			wantPath: "core-practices",
		},
		{
			name:    "fragments no longer supported",
			input:   "file:///var/lib/ctxloom/repos/main@fragments/security/aws",
			wantErr: true,
		},
		{
			name:    "missing version",
			input:   "file:///home/user/repo/bundles/core",
			wantErr: true,
		},
		{
			// A non-empty host in a file:// URL used to be silently
			// dropped (u.Path only), so "file://host/path@bundles/x" resolved
			// to "file:///path" — a DIFFERENT, local repository — instead of
			// erroring. file:// support here is local-repository-only.
			name:    "host is rejected, not silently dropped",
			input:   "file://host/path@bundles/x",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReference(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseReference(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseReference(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", got.URL, tt.wantURL)
			}
			if got.ItemType != tt.wantType {
				t.Errorf("ItemType = %q, want %q", got.ItemType, tt.wantType)
			}
			if got.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", got.Path, tt.wantPath)
			}
			if !got.IsCanonical() {
				t.Errorf("IsCanonical = false, want true for file reference")
			}
		})
	}
}

func TestReference_String(t *testing.T) {
	tests := []struct {
		name string
		ref  Reference
		want string
	}{
		{
			name: "canonical HTTPS bundle",
			ref: Reference{
				URL:      "https://github.com/owner/repo",
				ItemType: ItemTypeBundle,
				Path:     "core-practices",
			},
			want: "ctxloom+git://github.com/owner/repo//bundles/core-practices",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReference_BuildFilePath(t *testing.T) {
	tests := []struct {
		name     string
		ref      Reference
		itemType ItemType
		want     string
	}{
		{
			name:     "non-canonical bundle uses passed item type",
			ref:      Reference{Path: "go-tools"},
			itemType: ItemTypeBundle,
			want:     ".ctxloom/content/bundles/go-tools.yaml",
		},
		{
			name:     "nested path",
			ref:      Reference{Path: "golang/best-practices"},
			itemType: ItemTypeBundle,
			want:     ".ctxloom/content/bundles/golang/best-practices.yaml",
		},
		{
			name: "canonical uses embedded values",
			ref: Reference{
				URL:      "https://github.com/owner/repo",
				ItemType: ItemTypeBundle,
				Path:     "core-practices",
			},
			itemType: ItemTypeBundle, // Passed item type is ignored for canonical
			want:     ".ctxloom/content/bundles/core-practices.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.BuildFilePath(tt.itemType); got != tt.want {
				t.Errorf("BuildFilePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReference_LocalPath(t *testing.T) {
	tests := []struct {
		name     string
		ref      Reference
		baseDir  string
		itemType ItemType
		want     string
	}{
		{
			name: "canonical HTTPS bundle",
			ref: Reference{
				URL:      "https://github.com/ctxloom/ctxloom-github",
				ItemType: ItemTypeBundle,
				Path:     "core-practices",
			},
			baseDir:  ".ctxloom",
			itemType: ItemTypeBundle, // Passed item type is ignored for canonical
			want:     ".ctxloom/cache/bundles/github.com/ctxloom/ctxloom-github/core-practices.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.LocalPath(tt.baseDir, tt.itemType); got != tt.want {
				t.Errorf("LocalPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReference_LocalRemoteName(t *testing.T) {
	tests := []struct {
		name string
		ref  Reference
		want string
	}{
		{
			name: "URL-less ref has no remote name",
			ref:  Reference{},
			want: "",
		},
		{
			name: "HTTPS URL",
			ref: Reference{
				URL: "https://github.com/owner/repo",
			},
			want: "github.com/owner/repo",
		},
		{
			name: "SSH URL",
			ref: Reference{
				URL: "git@github.com:owner/repo",
			},
			want: "github.com/owner/repo",
		},
		{
			name: "file URL",
			ref: Reference{
				URL: "file:///home/user/ctxloom-content",
			},
			want: "user/ctxloom-content",
		},
		{
			name: "file URL with single path component",
			ref: Reference{
				URL: "file:///repo",
			},
			want: "repo",
		},
		{
			name: "malformed URL falls back to sanitize",
			ref: Reference{
				URL: "unknown://weird:url",
			},
			want: "unknown/weird/url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.LocalRemoteName(); got != tt.want {
				t.Errorf("LocalRemoteName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractRepoName(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		want    string
	}{
		{
			name:    "HTTPS GitHub URL",
			repoURL: "https://github.com/owner/repo",
			want:    "repo",
		},
		{
			name:    "HTTPS GitLab URL with subgroups",
			repoURL: "https://gitlab.com/group/subgroup/repo",
			want:    "repo",
		},
		{
			name:    "HTTP URL",
			repoURL: "http://example.com/owner/my-repo",
			want:    "my-repo",
		},
		{
			name:    "SSH GitHub URL",
			repoURL: "git@github.com:owner/repo",
			want:    "repo",
		},
		{
			name:    "SSH GitLab URL with subgroups",
			repoURL: "git@gitlab.com:group/subgroup/repo",
			want:    "repo",
		},
		{
			name:    "file URL",
			repoURL: "file:///path/to/repo",
			want:    "repo",
		},
		{
			name:    "file URL with single component",
			repoURL: "file:///repo",
			want:    "repo",
		},
		{
			name:    "unknown format falls back to sanitize",
			repoURL: "unknown://weird:format",
			want:    "unknown/weird/format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractRepoName(tt.repoURL); got != tt.want {
				t.Errorf("ExtractRepoName(%q) = %q, want %q", tt.repoURL, got, tt.want)
			}
		})
	}
}

func TestReference_CanonicalString(t *testing.T) {
	tests := []struct {
		name string
		ref  Reference
		want string
	}{
		{
			name: "canonical bundle",
			ref: Reference{
				URL:      "https://github.com/owner/repo",
				ItemType: ItemTypeBundle,
				Path:     "core-practices",
			},
			want: "ctxloom+git://github.com/owner/repo//bundles/core-practices",
		},
		{
			// The canonical URI has one item-type marker ("bundles/") because
			// bundles are the only distributed item type, so an unset ItemType
			// cannot render a malformed marker the way the fetch address's
			// "<url>@<kind>s/<path>" interpolation can.
			name: "empty item type",
			ref: Reference{
				URL:      "https://github.com/owner/repo",
				ItemType: "",
				Path:     "core",
			},
			want: "ctxloom+git://github.com/owner/repo//bundles/core",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.CanonicalString(); got != tt.want {
				t.Errorf("CanonicalString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseReference_ContentVersion(t *testing.T) {
	tests := []struct {
		name               string
		input              string
		wantContentVersion string
		wantPath           string
	}{
		{
			name:               "HTTPS with content version tag",
			input:              "https://github.com/owner/repo@bundles/core@v1.2.3",
			wantContentVersion: "v1.2.3",
			wantPath:           "core",
		},
		{
			name:               "HTTPS with content version SHA",
			input:              "https://github.com/owner/repo@bundles/core@abc1234",
			wantContentVersion: "abc1234",
			wantPath:           "core",
		},
		{
			name:               "SSH with content version",
			input:              "git@github.com:owner/repo@bundles/dev@v2.0.0",
			wantContentVersion: "v2.0.0",
			wantPath:           "dev",
		},
		{
			name:               "file URL with content version",
			input:              "file:///path/to/repo@bundles/tools@main",
			wantContentVersion: "main",
			wantPath:           "tools",
		},
		{
			name:               "without content version",
			input:              "https://github.com/owner/repo@bundles/core",
			wantContentVersion: "",
			wantPath:           "core",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := ParseReference(tt.input)
			if err != nil {
				t.Fatalf("ParseReference(%q) unexpected error: %v", tt.input, err)
			}
			if ref.ContentVersion != tt.wantContentVersion {
				t.Errorf("ContentVersion = %q, want %q", ref.ContentVersion, tt.wantContentVersion)
			}
			if ref.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", ref.Path, tt.wantPath)
			}
		})
	}
}

// ResolveRefString emits "<url>@<kind>/<path>@<hash>#<selector>" — the version
// BEFORE the selector — while authored refs may put the selector first. Both
// orderings must keep their version and keep the selector out of Path.
func TestParseReference_SelectorVersionOrderings(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantPath    string
		wantVersion string
	}{
		{
			name:        "sha before selector",
			input:       "https://github.com/o/r@bundles/demo@abc123#fragments/x",
			wantPath:    "demo",
			wantVersion: "abc123",
		},
		{
			name:        "semver before selector",
			input:       "https://github.com/o/r@bundles/demo@v1.2.0#fragments/x",
			wantPath:    "demo",
			wantVersion: "v1.2.0",
		},
		{
			name:        "selector before sha",
			input:       "https://github.com/o/r@bundles/demo#fragments/x@abc123",
			wantPath:    "demo",
			wantVersion: "abc123",
		},
		{
			name:        "selector without version",
			input:       "https://github.com/o/r@bundles/demo#fragments/x",
			wantPath:    "demo",
			wantVersion: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReference(tt.input)
			if err != nil {
				t.Fatalf("ParseReference(%q) unexpected error: %v", tt.input, err)
			}
			if got.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", got.Path, tt.wantPath)
			}
			if got.ContentVersion != tt.wantVersion {
				t.Errorf("ContentVersion = %q, want %q", got.ContentVersion, tt.wantVersion)
			}
		})
	}
}

// TestParseReference_RejectsTraversal pins the parse-time path-traversal gate:
// an item path is later joined under a repo root (BuildFilePath) and, for
// filesystem-backed sources, under a directory root (fsVCS.ReadFile), so ".."
// segments and absolute paths must be rejected at parse time with a clear
// error rather than contained ad hoc at each read site.
func TestParseReference_RejectsTraversal(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string // "" means the reference must parse
	}{
		{
			name:    "dotdot escape in https ref",
			input:   "https://github.com/o/r@bundles/../../../etc/passwd",
			wantErr: "not allowed",
		},
		{
			name:    "interior dotdot segment",
			input:   "https://github.com/o/r@bundles/sub/../other",
			wantErr: "not allowed",
		},
		{
			name:    "trailing dotdot segment",
			input:   "https://github.com/o/r@bundles/..",
			wantErr: "not allowed",
		},
		{
			name:    "dot segment",
			input:   "https://github.com/o/r@bundles/./demo",
			wantErr: "not allowed",
		},
		{
			name:    "absolute item path",
			input:   "https://github.com/o/r@bundles//etc/passwd",
			wantErr: "not allowed",
		},
		{
			name:    "backslash dotdot segment",
			input:   `https://github.com/o/r@bundles/..\demo`,
			wantErr: "not allowed",
		},
		{
			name:    "dotdot in local ref",
			input:   "ctxloom:local@bundles/../secrets",
			wantErr: "not allowed",
		},
		{
			name:    "dotdot in ssh ref",
			input:   "git@github.com:o/r@bundles/../x",
			wantErr: "not allowed",
		},
		{
			name:  "plain nested path still parses",
			input: "https://github.com/o/r@bundles/lang/go/testing",
		},
		{
			name:  "dotfile name (not a traversal) still parses",
			input: "https://github.com/o/r@bundles/.hidden",
		},
		{
			name:  "double-dot prefix in a name still parses",
			input: "https://github.com/o/r@bundles/..weird",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReference(tt.input)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseReference(%q) unexpected error: %v", tt.input, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseReference(%q) = %+v, want traversal error", tt.input, got)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err.Error(), tt.wantErr)
			}
		})
	}
}
