package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestProfileSelector pins the "<bundle>#profiles/<name>" grammar — the profile
// counterpart to FragmentSelector ("#fragments/") and PromptSelector
// ("#skills/") — so bundle profiles are addressed consistently with the other
// bundle item kinds.
func TestProfileSelector(t *testing.T) {
	assert.Equal(t, "#profiles/", ProfileSelector)
}

func TestBundleProfileRef(t *testing.T) {
	tests := []struct {
		name   string
		bundle string
		prof   string
		want   string
	}{
		{
			name:   "local bundle canonicalizes to ctxloom:local",
			bundle: "code-review",
			prof:   "cr-security-golang",
			want:   "ctxloom:local@bundles/code-review#profiles/cr-security-golang",
		},
		{
			name:   "already-canonical local ref passes through",
			bundle: "ctxloom:local@bundles/code-review",
			prof:   "base",
			want:   "ctxloom:local@bundles/code-review#profiles/base",
		},
		{
			name:   "remote canonical bundle ref",
			bundle: "https://github.com/ctxloom/ctxloom-default@bundles/code-review",
			prof:   "cr-reliability-swift",
			want:   "https://github.com/ctxloom/ctxloom-default@bundles/code-review#profiles/cr-reliability-swift",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BundleProfileRef(tt.bundle, tt.prof))
		})
	}
}

func TestSplitBundleProfileRef(t *testing.T) {
	tests := []struct {
		name       string
		ref        string
		wantBundle string
		wantName   string
		wantOK     bool
	}{
		{
			name:       "bundle profile ref splits",
			ref:        "ctxloom:local@bundles/code-review#profiles/cr-security-golang",
			wantBundle: "ctxloom:local@bundles/code-review",
			wantName:   "cr-security-golang",
			wantOK:     true,
		},
		{
			name:       "remote bundle profile ref splits",
			ref:        "https://github.com/o/r@bundles/code-review#profiles/base",
			wantBundle: "https://github.com/o/r@bundles/code-review",
			wantName:   "base",
			wantOK:     true,
		},
		{
			name:   "top-level remote profile ref is NOT a bundle profile",
			ref:    "https://github.com/o/r@profiles/dev",
			wantOK: false,
		},
		{
			name:   "plain local profile name is NOT a bundle profile",
			ref:    "go-developer",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle, name, ok := SplitBundleProfileRef(tt.ref)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantBundle, bundle)
				assert.Equal(t, tt.wantName, name)
			}
		})
	}
}

// TestBundleProfileRef_RoundTrip confirms BundleProfileRef and
// SplitBundleProfileRef are inverses for a canonical bundle ref.
func TestBundleProfileRef_RoundTrip(t *testing.T) {
	const bundle = "https://github.com/o/r@bundles/code-review"
	ref := BundleProfileRef(bundle, "cr-correctness-rust")
	gotBundle, gotName, ok := SplitBundleProfileRef(ref)
	assert.True(t, ok)
	assert.Equal(t, bundle, gotBundle)
	assert.Equal(t, "cr-correctness-rust", gotName)
}
