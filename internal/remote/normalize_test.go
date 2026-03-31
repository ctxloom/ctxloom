package remote

import (
	"testing"
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
			input: "https://github.com/owner/ctxloom-github@v1/bundles/core-practices",
			want:  "ctxloom-github/core-practices",
		},
		{
			name:  "https canonical URL with content version",
			input: "https://github.com/owner/ctxloom-github@v1/bundles/core-practices@v1.2.3",
			want:  "ctxloom-github/core-practices",
		},
		{
			name:  "https canonical URL for profile",
			input: "https://github.com/owner/ctxloom-github@v1/profiles/rust-developer",
			want:  "ctxloom-github/rust-developer",
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
			input: "https://github.com/owner/ctxloom-github@v1/bundles/core#fragments/coding",
			want:  "ctxloom-github/core#fragments/coding",
		},
		{
			name:  "canonical URL with prompt path",
			input: "https://github.com/owner/ctxloom-github@v1/bundles/core#prompts/review",
			want:  "ctxloom-github/core#prompts/review",
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
		{"ctxloom-github/core-practices", false},
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

