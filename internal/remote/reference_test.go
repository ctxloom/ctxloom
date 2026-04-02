package remote

import (
	"testing"
)

func TestParseReference_Simple(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Reference
		wantErr bool
	}{
		{
			name:  "simple reference",
			input: "alice/security",
			want: &Reference{
				Remote: "alice",
				Path:   "security",
				ContentVersion: "",
			},
		},
		{
			name:  "reference with tag",
			input: "alice/security@v1.0.0",
			want: &Reference{
				Remote: "alice",
				Path:   "security",
				ContentVersion: "v1.0.0",
			},
		},
		{
			name:  "reference with SHA",
			input: "alice/security@abc1234",
			want: &Reference{
				Remote: "alice",
				Path:   "security",
				ContentVersion: "abc1234",
			},
		},
		{
			name:  "nested path",
			input: "alice/golang/best-practices",
			want: &Reference{
				Remote: "alice",
				Path:   "golang/best-practices",
				ContentVersion: "",
			},
		},
		{
			name:  "nested path with tag",
			input: "alice/golang/best-practices@v2.0.0",
			want: &Reference{
				Remote: "alice",
				Path:   "golang/best-practices",
				ContentVersion: "v2.0.0",
			},
		},
		{
			name:  "deeply nested path",
			input: "corp/lang/go/testing/mocks@main",
			want: &Reference{
				Remote: "corp",
				Path:   "lang/go/testing/mocks",
				ContentVersion: "main",
			},
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "no slash",
			input:   "alice",
			wantErr: true,
		},
		{
			name:    "empty remote",
			input:   "/security",
			wantErr: true,
		},
		{
			name:    "empty path",
			input:   "alice/",
			wantErr: true,
		},
		{
			name:  "at sign in path (edge case)",
			input: "alice/email@domain@v1.0.0",
			want: &Reference{
				Remote: "alice",
				Path:   "email@domain",
				ContentVersion: "v1.0.0",
			},
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
			if got.Remote != tt.want.Remote {
				t.Errorf("Remote = %q, want %q", got.Remote, tt.want.Remote)
			}
			if got.Path != tt.want.Path {
				t.Errorf("Path = %q, want %q", got.Path, tt.want.Path)
			}
			if got.ContentVersion != tt.want.ContentVersion {
				t.Errorf("ContentVersion = %q, want %q", got.ContentVersion, tt.want.ContentVersion)
			}
			if got.IsCanonical {
				t.Errorf("IsCanonical = true, want false for simple reference")
			}
		})
	}
}

func TestParseReference_HTTPS(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantURL  string
		wantVer  string
		wantType ItemType
		wantPath string
		wantErr  bool
	}{
		{
			name:     "github bundle",
			input:    "https://github.com/owner/repo@v1/bundles/core-practices",
			wantURL:  "https://github.com/owner/repo",
			wantVer:  "v1",
			wantType: ItemTypeBundle,
			wantPath: "core-practices",
		},
		{
			name:     "nested path",
			input:    "https://github.com/ctxloom/ctxloom-github@v1/bundles/golang/testing",
			wantURL:  "https://github.com/ctxloom/ctxloom-github",
			wantVer:  "v1",
			wantType: ItemTypeBundle,
			wantPath: "golang/testing",
		},
		{
			name:     "profile type",
			input:    "https://github.com/owner/repo@v1/profiles/go-developer",
			wantURL:  "https://github.com/owner/repo",
			wantVer:  "v1",
			wantType: ItemTypeProfile,
			wantPath: "go-developer",
		},
		{
			name:    "fragments no longer supported",
			input:   "https://gitlab.com/group/project@v2/fragments/security",
			wantErr: true,
		},
		{
			name:    "prompts no longer supported",
			input:   "https://github.com/owner/repo@v1/prompts/code-review",
			wantErr: true,
		},
		{
			name:    "mcp-servers no longer supported",
			input:   "https://github.com/owner/repo@v1/mcp-servers/sequential-thinking",
			wantErr: true,
		},
		{
			name:    "missing version",
			input:   "https://github.com/owner/repo/bundles/core",
			wantErr: true,
		},
		{
			name:    "missing type",
			input:   "https://github.com/owner/repo@v1/core",
			wantErr: true,
		},
		{
			name:    "invalid type",
			input:   "https://github.com/owner/repo@v1/invalid/core",
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
			if got.Version != tt.wantVer {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVer)
			}
			if got.ItemType != tt.wantType {
				t.Errorf("ItemType = %q, want %q", got.ItemType, tt.wantType)
			}
			if got.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", got.Path, tt.wantPath)
			}
			if !got.IsCanonical {
				t.Errorf("IsCanonical = false, want true for URL reference")
			}
		})
	}
}

func TestParseReference_SSH(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantURL  string
		wantVer  string
		wantType ItemType
		wantPath string
		wantErr  bool
	}{
		{
			name:     "github ssh bundle",
			input:    "git@github.com:owner/repo@v1/bundles/core-practices",
			wantURL:  "git@github.com:owner/repo",
			wantVer:  "v1",
			wantType: ItemTypeBundle,
			wantPath: "core-practices",
		},
		{
			name:     "gitlab ssh profile",
			input:    "git@gitlab.com:group/subgroup/repo@v2/profiles/security",
			wantURL:  "git@gitlab.com:group/subgroup/repo",
			wantVer:  "v2",
			wantType: ItemTypeProfile,
			wantPath: "security",
		},
		{
			name:    "fragments no longer supported",
			input:   "git@gitlab.com:group/subgroup/repo@v2/fragments/security",
			wantErr: true,
		},
		{
			name:    "missing version",
			input:   "git@github.com:owner/repo/bundles/core",
			wantErr: true,
		},
		{
			name:    "missing colon",
			input:   "git@github.com/owner/repo@v1/bundles/core",
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
			if got.Version != tt.wantVer {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVer)
			}
			if got.ItemType != tt.wantType {
				t.Errorf("ItemType = %q, want %q", got.ItemType, tt.wantType)
			}
			if got.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", got.Path, tt.wantPath)
			}
			if !got.IsCanonical {
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
		wantVer  string
		wantType ItemType
		wantPath string
		wantErr  bool
	}{
		{
			name:     "absolute path bundle",
			input:    "file:///home/user/ctxloom-content@v1/bundles/core-practices",
			wantURL:  "file:///home/user/ctxloom-content",
			wantVer:  "v1",
			wantType: ItemTypeBundle,
			wantPath: "core-practices",
		},
		{
			name:     "deep path profile",
			input:    "file:///var/lib/ctxloom/repos/main@v2/profiles/security-aws",
			wantURL:  "file:///var/lib/ctxloom/repos/main",
			wantVer:  "v2",
			wantType: ItemTypeProfile,
			wantPath: "security-aws",
		},
		{
			name:    "fragments no longer supported",
			input:   "file:///var/lib/ctxloom/repos/main@v2/fragments/security/aws",
			wantErr: true,
		},
		{
			name:    "missing version",
			input:   "file:///home/user/repo/bundles/core",
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
			if got.Version != tt.wantVer {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVer)
			}
			if got.ItemType != tt.wantType {
				t.Errorf("ItemType = %q, want %q", got.ItemType, tt.wantType)
			}
			if got.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", got.Path, tt.wantPath)
			}
			if !got.IsCanonical {
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
			name: "simple without git ref",
			ref:  Reference{Remote: "alice", Path: "security", ContentVersion: ""},
			want: "alice/security",
		},
		{
			name: "simple with git ref",
			ref:  Reference{Remote: "alice", Path: "security", ContentVersion: "v1.0.0"},
			want: "alice/security@v1.0.0",
		},
		{
			name: "nested path with ref",
			ref:  Reference{Remote: "corp", Path: "go/testing", ContentVersion: "main"},
			want: "corp/go/testing@main",
		},
		{
			name: "canonical HTTPS bundle",
			ref: Reference{
				URL:         "https://github.com/owner/repo",
				Version:     "v1",
				ItemType:    ItemTypeBundle,
				Path:        "core-practices",
				IsCanonical: true,
			},
			want: "https://github.com/owner/repo@v1/bundles/core-practices",
		},
		{
			name: "canonical SSH profile",
			ref: Reference{
				URL:         "git@github.com:owner/repo",
				Version:     "v2",
				ItemType:    ItemTypeProfile,
				Path:        "security",
				IsCanonical: true,
			},
			want: "git@github.com:owner/repo@v2/profiles/security",
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
		version  string
		want     string
	}{
		{
			name:     "simple bundle",
			ref:      Reference{Remote: "alice", Path: "go-tools"},
			itemType: ItemTypeBundle,
			version:  "v1",
			want:     "ctxloom/v1/bundles/go-tools.yaml",
		},
		{
			name:     "simple profile",
			ref:      Reference{Remote: "alice", Path: "security-focused"},
			itemType: ItemTypeProfile,
			version:  "v1",
			want:     "ctxloom/v1/profiles/security-focused.yaml",
		},
		{
			name:     "nested bundle",
			ref:      Reference{Remote: "alice", Path: "golang/best-practices"},
			itemType: ItemTypeBundle,
			version:  "v2",
			want:     "ctxloom/v2/bundles/golang/best-practices.yaml",
		},
		{
			name: "canonical uses embedded values",
			ref: Reference{
				URL:         "https://github.com/owner/repo",
				Version:     "v3",
				ItemType:    ItemTypeBundle,
				Path:        "core-practices",
				IsCanonical: true,
			},
			itemType: ItemTypeProfile, // Should be ignored for canonical
			version:  "v1",            // Should be ignored for canonical
			want:     "ctxloom/v3/bundles/core-practices.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.BuildFilePath(tt.itemType, tt.version); got != tt.want {
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
			name:     "simple bundle",
			ref:      Reference{Remote: "alice", Path: "go-tools"},
			baseDir:  "/home/user/.ctxloom",
			itemType: ItemTypeBundle,
			want:     "/home/user/.ctxloom/cache/bundles/alice/go-tools.yaml",
		},
		{
			name:     "simple profile",
			ref:      Reference{Remote: "corp", Path: "security"},
			baseDir:  ".ctxloom",
			itemType: ItemTypeProfile,
			want:     ".ctxloom/profiles/corp/security.yaml",
		},
		{
			name:     "nested path",
			ref:      Reference{Remote: "alice", Path: "lang/go/testing"},
			baseDir:  "/home/user/.ctxloom",
			itemType: ItemTypeBundle,
			want:     "/home/user/.ctxloom/cache/bundles/alice/lang/go/testing.yaml",
		},
		{
			name: "canonical HTTPS bundle",
			ref: Reference{
				URL:         "https://github.com/ctxloom/ctxloom-github",
				Version:     "v1",
				ItemType:    ItemTypeBundle,
				Path:        "core-practices",
				IsCanonical: true,
			},
			baseDir:  ".ctxloom",
			itemType: ItemTypeProfile, // Should be ignored for canonical
			want:     ".ctxloom/cache/bundles/github.com/ctxloom/ctxloom-github/core-practices.yaml",
		},
		{
			name: "canonical SSH profile",
			ref: Reference{
				URL:         "git@github.com:owner/repo",
				Version:     "v1",
				ItemType:    ItemTypeProfile,
				Path:        "security",
				IsCanonical: true,
			},
			baseDir:  ".ctxloom",
			itemType: ItemTypeBundle, // Should be ignored for canonical
			want:     ".ctxloom/profiles/github.com/owner/repo/security.yaml",
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
			name: "simple remote",
			ref:  Reference{Remote: "alice"},
			want: "alice",
		},
		{
			name: "HTTPS URL",
			ref: Reference{
				URL:         "https://github.com/owner/repo",
				IsCanonical: true,
			},
			want: "github.com/owner/repo",
		},
		{
			name: "SSH URL",
			ref: Reference{
				URL:         "git@github.com:owner/repo",
				IsCanonical: true,
			},
			want: "github.com/owner/repo",
		},
		{
			name: "file URL",
			ref: Reference{
				URL:         "file:///home/user/ctxloom-content",
				IsCanonical: true,
			},
			want: "user/ctxloom-content",
		},
		{
			name: "file URL with single path component",
			ref: Reference{
				URL:         "file:///repo",
				IsCanonical: true,
			},
			want: "repo",
		},
		{
			name: "malformed URL falls back to sanitize",
			ref: Reference{
				URL:         "unknown://weird:url",
				IsCanonical: true,
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

func TestReference_RepoURL(t *testing.T) {
	tests := []struct {
		name string
		ref  Reference
		want string
	}{
		{
			name: "simple reference has no URL",
			ref:  Reference{Remote: "alice", Path: "security"},
			want: "",
		},
		{
			name: "canonical HTTPS reference",
			ref: Reference{
				URL:         "https://github.com/owner/repo",
				IsCanonical: true,
			},
			want: "https://github.com/owner/repo",
		},
		{
			name: "canonical SSH reference",
			ref: Reference{
				URL:         "git@github.com:owner/repo",
				IsCanonical: true,
			},
			want: "git@github.com:owner/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.RepoURL(); got != tt.want {
				t.Errorf("RepoURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReference_ToLocalName(t *testing.T) {
	tests := []struct {
		name string
		ref  Reference
		want string
	}{
		{
			name: "simple reference",
			ref:  Reference{Remote: "alice", Path: "security"},
			want: "alice/security",
		},
		{
			name: "canonical HTTPS reference",
			ref: Reference{
				URL:         "https://github.com/owner/ctxloom-github",
				Version:     "v1",
				ItemType:    ItemTypeBundle,
				Path:        "core-practices",
				IsCanonical: true,
			},
			want: "ctxloom-github/core-practices",
		},
		{
			name: "canonical SSH reference",
			ref: Reference{
				URL:         "git@github.com:owner/my-repo",
				Version:     "v1",
				ItemType:    ItemTypeProfile,
				Path:        "dev",
				IsCanonical: true,
			},
			want: "my-repo/dev",
		},
		{
			name: "canonical file reference",
			ref: Reference{
				URL:         "file:///home/user/my-ctxloom",
				Version:     "v1",
				ItemType:    ItemTypeBundle,
				Path:        "tools",
				IsCanonical: true,
			},
			want: "my-ctxloom/tools",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.ToLocalName(); got != tt.want {
				t.Errorf("ToLocalName() = %q, want %q", got, tt.want)
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

func TestReference_ToCanonicalWithVersion(t *testing.T) {
	tests := []struct {
		name string
		ref  Reference
		want string
	}{
		{
			name: "simple reference without URL",
			ref:  Reference{Remote: "alice", Path: "security", ContentVersion: "v1.0.0"},
			want: "alice/security@v1.0.0",
		},
		{
			name: "canonical without content version",
			ref: Reference{
				URL:         "https://github.com/owner/repo",
				Version:     "v1",
				ItemType:    ItemTypeBundle,
				Path:        "core-practices",
				IsCanonical: true,
			},
			want: "https://github.com/owner/repo@v1/bundles/core-practices",
		},
		{
			name: "canonical with content version",
			ref: Reference{
				URL:            "https://github.com/owner/repo",
				Version:        "v1",
				ItemType:       ItemTypeBundle,
				Path:           "core-practices",
				ContentVersion: "v1.2.3",
				IsCanonical:    true,
			},
			want: "https://github.com/owner/repo@v1/bundles/core-practices@v1.2.3",
		},
		{
			name: "canonical profile with SHA content version",
			ref: Reference{
				URL:            "git@github.com:owner/repo",
				Version:        "v2",
				ItemType:       ItemTypeProfile,
				Path:           "security",
				ContentVersion: "abc1234",
				IsCanonical:    true,
			},
			want: "git@github.com:owner/repo@v2/profiles/security@abc1234",
		},
		{
			name: "canonical with empty item type",
			ref: Reference{
				URL:         "https://github.com/owner/repo",
				Version:     "v1",
				ItemType:    "",
				Path:        "core",
				IsCanonical: true,
			},
			want: "https://github.com/owner/repo@v1/s/core",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.ToCanonicalWithVersion(); got != tt.want {
				t.Errorf("ToCanonicalWithVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReference_EffectiveContentVersion(t *testing.T) {
	tests := []struct {
		name string
		ref  Reference
		want string
	}{
		{
			name: "returns ContentVersion",
			ref: Reference{
				ContentVersion: "v1.2.3",
			},
			want: "v1.2.3",
		},
		{
			name: "empty returns empty",
			ref: Reference{
				ContentVersion: "",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.EffectiveContentVersion(); got != tt.want {
				t.Errorf("EffectiveContentVersion() = %q, want %q", got, tt.want)
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
			name: "simple reference returns String format",
			ref:  Reference{Remote: "alice", Path: "security"},
			want: "alice/security",
		},
		{
			name: "canonical bundle",
			ref: Reference{
				URL:         "https://github.com/owner/repo",
				Version:     "v1",
				ItemType:    ItemTypeBundle,
				Path:        "core-practices",
				IsCanonical: true,
			},
			want: "https://github.com/owner/repo@v1/bundles/core-practices",
		},
		{
			name: "canonical profile",
			ref: Reference{
				URL:         "git@github.com:owner/repo",
				Version:     "v2",
				ItemType:    ItemTypeProfile,
				Path:        "security",
				IsCanonical: true,
			},
			want: "git@github.com:owner/repo@v2/profiles/security",
		},
		{
			name: "empty item type",
			ref: Reference{
				URL:         "https://github.com/owner/repo",
				Version:     "v1",
				ItemType:    "",
				Path:        "core",
				IsCanonical: true,
			},
			want: "https://github.com/owner/repo@v1/s/core",
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
			input:              "https://github.com/owner/repo@v1/bundles/core@v1.2.3",
			wantContentVersion: "v1.2.3",
			wantPath:           "core",
		},
		{
			name:               "HTTPS with content version SHA",
			input:              "https://github.com/owner/repo@v1/bundles/core@abc1234",
			wantContentVersion: "abc1234",
			wantPath:           "core",
		},
		{
			name:               "SSH with content version",
			input:              "git@github.com:owner/repo@v1/profiles/dev@v2.0.0",
			wantContentVersion: "v2.0.0",
			wantPath:           "dev",
		},
		{
			name:               "file URL with content version",
			input:              "file:///path/to/repo@v1/bundles/tools@main",
			wantContentVersion: "main",
			wantPath:           "tools",
		},
		{
			name:               "without content version",
			input:              "https://github.com/owner/repo@v1/bundles/core",
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

func TestReference_ToCanonical(t *testing.T) {
	tests := []struct {
		name     string
		ref      *Reference
		itemType ItemType
		wantURL  string
		wantErr  bool
	}{
		{
			name: "converts simple ref to canonical",
			ref: &Reference{
				Remote:      "alice",
				Path:        "security",
				ContentVersion:      "v1.0.0",
				IsCanonical: false,
			},
			itemType: ItemTypeBundle,
			wantURL:  "https://github.com/alice/ctxloom",
			wantErr:  false,
		},
		{
			name: "already canonical returns same",
			ref: &Reference{
				URL:         "https://github.com/owner/repo",
				Version:     "v1",
				ItemType:    ItemTypeBundle,
				Path:        "security",
				IsCanonical: true,
			},
			itemType: ItemTypeBundle,
			wantURL:  "https://github.com/owner/repo",
			wantErr:  false,
		},
		{
			name: "unknown remote returns error",
			ref: &Reference{
				Remote:      "unknown",
				Path:        "security",
				IsCanonical: false,
			},
			itemType: ItemTypeBundle,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create registry with alice remote
			registry, _ := NewRegistry("")
			_ = registry.Add("alice", "https://github.com/alice/ctxloom")

			result, err := tt.ref.ToCanonical(registry, tt.itemType)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", result.URL, tt.wantURL)
			}
			if !result.IsCanonical {
				t.Error("expected IsCanonical to be true")
			}
		})
	}
}

func TestReference_MustCanonical(t *testing.T) {
	t.Run("succeeds for valid remote", func(t *testing.T) {
		registry, _ := NewRegistry("")
		_ = registry.Add("alice", "https://github.com/alice/ctxloom")

		ref := &Reference{
			Remote:      "alice",
			Path:        "security",
			IsCanonical: false,
		}

		// Should not panic
		result := ref.MustCanonical(registry, ItemTypeBundle)
		if !result.IsCanonical {
			t.Error("expected canonical reference")
		}
	})

	t.Run("panics for unknown remote", func(t *testing.T) {
		registry, _ := NewRegistry("")

		ref := &Reference{
			Remote:      "unknown",
			Path:        "security",
			IsCanonical: false,
		}

		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic but got none")
			}
		}()

		ref.MustCanonical(registry, ItemTypeBundle)
	})
}

