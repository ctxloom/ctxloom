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

func TestCanonicalBundleRef(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"dev", "ctxloom:local@bundles/dev"},
		{"team/dev", "ctxloom:local@bundles/team/dev"},
		{"ctxloom:local@bundles/dev", "ctxloom:local@bundles/dev"},
		{"ctxloom:local@bundles/dev@rev1", "ctxloom:local@bundles/dev"},
		{"https://github.com/o/r@bundles/demo", "https://github.com/o/r@bundles/demo"},
		{"https://github.com/o/r@bundles/demo@abc123", "https://github.com/o/r@bundles/demo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := CanonicalBundleRef(tt.input)
			if err != nil {
				t.Fatalf("CanonicalBundleRef(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("CanonicalBundleRef(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCanonicalFragmentRef(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"dev#fragments/x", "ctxloom:local@bundles/dev#fragments/x"},
		{"ctxloom:local@bundles/dev#fragments/x", "ctxloom:local@bundles/dev#fragments/x"},
		{"https://github.com/o/r@bundles/demo#fragments/x", "https://github.com/o/r@bundles/demo#fragments/x"},
		{"https://github.com/o/r@bundles/demo@abc123#fragments/x", "https://github.com/o/r@bundles/demo#fragments/x"},
		// No fragment selector → unchanged (bare names, prompt selectors).
		{"x", "x"},
		{"dev#commands/x", "dev#commands/x"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := CanonicalFragmentRef(tt.input)
			if err != nil {
				t.Fatalf("CanonicalFragmentRef(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("CanonicalFragmentRef(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSplitPromptVersion(t *testing.T) {
	tests := []struct {
		input       string
		wantCanon   string
		wantVersion string
	}{
		// Trailing "@<commit>" (the name-addressed CLI/resource form).
		{"dev#commands/x@c1", "ctxloom:local@bundles/dev#commands/x", "c1"},
		{"https://github.com/o/r@bundles/demo#commands/x@abc123", "https://github.com/o/r@bundles/demo#commands/x", "abc123"},
		// Version on the bundle part is also honored.
		{"https://github.com/o/r@bundles/demo@abc123#commands/x", "https://github.com/o/r@bundles/demo#commands/x", "abc123"},
		// Unversioned qualified ref → canonicalized, empty version.
		{"dev#commands/x", "ctxloom:local@bundles/dev#commands/x", ""},
		// No command selector → unchanged, empty version (bare names, fragment selectors).
		{"x", "x", ""},
		{"dev#fragments/x", "dev#fragments/x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			canon, version, err := SplitPromptVersion(tt.input)
			if err != nil {
				t.Fatalf("SplitPromptVersion(%q): %v", tt.input, err)
			}
			if canon != tt.wantCanon || version != tt.wantVersion {
				t.Errorf("SplitPromptVersion(%q) = (%q, %q), want (%q, %q)",
					tt.input, canon, version, tt.wantCanon, tt.wantVersion)
			}
		})
	}
}

func TestFragmentName(t *testing.T) {
	if name, ok := FragmentName("dev#fragments/x"); !ok || name != "x" {
		t.Errorf("FragmentName(dev#fragments/x) = %q, %v", name, ok)
	}
	if _, ok := FragmentName("dev"); ok {
		t.Error("FragmentName(dev) should not match")
	}
}
