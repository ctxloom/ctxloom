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
