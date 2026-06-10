package remote

import (
	"testing"
)

func TestIsCanonicalRef(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://github.com/owner/repo@bundles/core", true},
		{"http://github.com/owner/repo@bundles/core", true},
		{"git@github.com:owner/repo@bundles/core", true},
		{"file:///path/to/repo@bundles/core", true},
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

// CanonicalKey strips a ref's content version to the version-less canonical
// form lockfiles and seeded-bundle maps key on.
func TestCanonicalKey(t *testing.T) {
	tests := []struct {
		input  string
		want   string
		wantOK bool
	}{
		{"https://github.com/o/r@bundles/demo@abc123", "https://github.com/o/r@bundles/demo", true},
		{"https://github.com/o/r@bundles/demo@abc123#fragments/x", "https://github.com/o/r@bundles/demo", true},
		{"https://github.com/o/r@profiles/dev@v1.2.0", "https://github.com/o/r@profiles/dev", true},
		{"https://github.com/o/r@bundles/demo", "https://github.com/o/r@bundles/demo", true},
		{"ctxloom:local@bundles/demo@rev1", "ctxloom:local@bundles/demo", true},
		{"plain-local-name", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := CanonicalKey(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("CanonicalKey(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("CanonicalKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
