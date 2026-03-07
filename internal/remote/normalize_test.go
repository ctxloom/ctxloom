package remote

import (
	"testing"

	"github.com/spf13/afero"
)

func TestToLocalRef(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		// Simple references (already local)
		{
			name:  "simple local ref",
			input: "alice/security",
			want:  "alice/security",
		},
		{
			name:  "simple local ref with version",
			input: "alice/security@v1.0.0",
			want:  "alice/security",
		},
		{
			name:  "nested path",
			input: "alice/golang/best-practices",
			want:  "alice/golang/best-practices",
		},

		// Canonical HTTPS URLs
		{
			name:  "https canonical URL",
			input: "https://github.com/owner/scm-github@v1/bundles/core-practices",
			want:  "scm-github/core-practices",
		},
		{
			name:  "https canonical URL with content version",
			input: "https://github.com/owner/scm-github@v1/bundles/core-practices@v1.2.3",
			want:  "scm-github/core-practices",
		},
		{
			name:  "https canonical URL for profile",
			input: "https://github.com/owner/scm-github@v1/profiles/rust-developer",
			want:  "scm-github/rust-developer",
		},
		{
			name:  "https canonical URL nested path",
			input: "https://github.com/owner/repo@v1/bundles/golang/testing",
			want:  "repo/golang/testing",
		},

		// Canonical SSH URLs
		{
			name:  "ssh canonical URL",
			input: "git@github.com:owner/my-repo@v1/bundles/core",
			want:  "my-repo/core",
		},
		{
			name:  "ssh canonical URL with content version",
			input: "git@github.com:owner/my-repo@v1/bundles/core@abc123",
			want:  "my-repo/core",
		},

		// With item path suffix (#fragments/name)
		{
			name:  "local ref with fragment path",
			input: "alice/security#fragments/auth",
			want:  "alice/security#fragments/auth",
		},
		{
			name:  "canonical URL with fragment path",
			input: "https://github.com/owner/scm-github@v1/bundles/core#fragments/coding",
			want:  "scm-github/core#fragments/coding",
		},
		{
			name:  "canonical URL with prompt path",
			input: "https://github.com/owner/scm-github@v1/bundles/core#prompts/review",
			want:  "scm-github/core#prompts/review",
		},

		// Error cases
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToLocalRef(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ToLocalRef() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ToLocalRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToCanonicalRef(t *testing.T) {
	// Create a test registry with a known remote
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "remotes.yaml", []byte(`
remotes:
  scm-github:
    url: https://github.com/owner/scm-github
    version: v1
  alice:
    url: https://github.com/alice/scm-content
    version: v1
`), 0644)

	registry, err := NewRegistry("remotes.yaml", WithRegistryFS(fs))
	if err != nil {
		t.Fatalf("failed to create registry: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		itemType ItemType
		want     string
		wantErr  bool
	}{
		{
			name:     "simple local ref to canonical",
			input:    "scm-github/core-practices",
			itemType: ItemTypeBundle,
			want:     "https://github.com/owner/scm-github@v1/bundles/core-practices",
		},
		{
			name:     "local ref to canonical profile",
			input:    "scm-github/rust-developer",
			itemType: ItemTypeProfile,
			want:     "https://github.com/owner/scm-github@v1/profiles/rust-developer",
		},
		{
			name:     "already canonical passthrough",
			input:    "https://github.com/owner/scm-github@v1/bundles/core",
			itemType: ItemTypeBundle,
			want:     "https://github.com/owner/scm-github@v1/bundles/core",
		},
		{
			name:     "local ref with fragment path",
			input:    "scm-github/core#fragments/auth",
			itemType: ItemTypeBundle,
			want:     "https://github.com/owner/scm-github@v1/bundles/core#fragments/auth",
		},
		{
			name:     "unknown remote",
			input:    "unknown/security",
			itemType: ItemTypeBundle,
			wantErr:  true,
		},
		{
			name:     "empty string",
			input:    "",
			itemType: ItemTypeBundle,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToCanonicalRef(tt.input, registry, tt.itemType)
			if (err != nil) != tt.wantErr {
				t.Errorf("ToCanonicalRef() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ToCanonicalRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeProfileBundles(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    []string
		wantErr bool
	}{
		{
			name: "mixed canonical and local refs",
			input: []string{
				"https://github.com/owner/scm-github@v1/bundles/core-practices",
				"alice/security",
				"https://github.com/owner/scm-github@v1/bundles/testing@v1.0.0",
			},
			want: []string{
				"scm-github/core-practices",
				"alice/security",
				"scm-github/testing",
			},
		},
		{
			name: "all local refs",
			input: []string{
				"alice/security",
				"bob/testing",
			},
			want: []string{
				"alice/security",
				"bob/testing",
			},
		},
		{
			name:  "empty slice",
			input: []string{},
			want:  []string{},
		},
		{
			name: "with empty strings filtered",
			input: []string{
				"alice/security",
				"",
				"bob/testing",
			},
			want: []string{
				"alice/security",
				"bob/testing",
			},
		},
		{
			name: "with fragment paths preserved",
			input: []string{
				"https://github.com/owner/repo@v1/bundles/core#fragments/auth",
				"alice/security#prompts/review",
			},
			want: []string{
				"repo/core#fragments/auth",
				"alice/security#prompts/review",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeProfileBundles(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("NormalizeProfileBundles() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("NormalizeProfileBundles() len = %d, want %d", len(got), len(tt.want))
					return
				}
				for i := range got {
					if got[i] != tt.want[i] {
						t.Errorf("NormalizeProfileBundles()[%d] = %q, want %q", i, got[i], tt.want[i])
					}
				}
			}
		})
	}
}

func TestIsCanonicalRef(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://github.com/owner/repo@v1/bundles/core", true},
		{"http://github.com/owner/repo@v1/bundles/core", true},
		{"git@github.com:owner/repo@v1/bundles/core", true},
		{"file:///path/to/repo@v1/bundles/core", true},
		{"alice/security", false},
		{"scm-github/core-practices", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsCanonicalRef(tt.input); got != tt.want {
				t.Errorf("IsCanonicalRef(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsLocalRef(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"alice/security", true},
		{"scm-github/core-practices", true},
		{"https://github.com/owner/repo@v1/bundles/core", false},
		{"git@github.com:owner/repo@v1/bundles/core", false},
		{"", true}, // Empty is not a URL
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsLocalRef(tt.input); got != tt.want {
				t.Errorf("IsLocalRef(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
