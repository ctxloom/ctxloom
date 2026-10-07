package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			want:   "ctxloom+local:code-review#profiles/cr-security-golang",
		},
		{
			name:   "already-canonical local ref passes through",
			bundle: "ctxloom:local@bundles/code-review",
			prof:   "base",
			want:   "ctxloom+local:code-review#profiles/base",
		},
		{
			name:   "remote canonical bundle ref",
			bundle: "https://github.com/ctxloom/ctxloom-default@bundles/code-review",
			prof:   "cr-reliability-swift",
			want:   "ctxloom+git://github.com/ctxloom/ctxloom-default//bundles/code-review#profiles/cr-reliability-swift",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BundleProfileRef(tt.bundle, tt.prof)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
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

// TestCanonicalProfileKey pins the selector-PRESERVING canonicalization of a
// bundle-profile ref — the regression here was resolving parents through
// CanonicalKey, which drops the "#profiles/<name>" selector and collapses the
// parent to its bundle, so every bundle-profile parent resolved as not found.
func TestCanonicalProfileKey(t *testing.T) {
	const repo = "https://github.com/ctxloom/ctxloom-default"
	const canonRepo = "ctxloom+git://github.com/ctxloom/ctxloom-default"
	tests := []struct {
		name   string
		ref    string
		want   string
		wantOK bool
	}{
		{
			name:   "plain bundle-profile ref is already canonical",
			ref:    repo + "@bundles/default#profiles/default",
			want:   canonRepo + "//bundles/default#profiles/default",
			wantOK: true,
		},
		{
			name:   "bundle-part version pin dropped",
			ref:    repo + "@bundles/default@abc1234#profiles/default",
			want:   canonRepo + "//bundles/default#profiles/default",
			wantOK: true,
		},
		{
			name:   "name-trailing version pin dropped",
			ref:    repo + "@bundles/default#profiles/default@abc1234",
			want:   canonRepo + "//bundles/default#profiles/default",
			wantOK: true,
		},
		{
			name:   "local bundle profile canonicalizes",
			ref:    "code-review#profiles/base",
			want:   "ctxloom+local:code-review#profiles/base",
			wantOK: true,
		},
		{
			name:   "plain bundle ref carries no profile selector",
			ref:    repo + "@bundles/default",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := CanonicalProfileKey(tt.ref)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// TestBundleProfileRef_RoundTrip confirms BundleProfileRef and
// SplitBundleProfileRef are inverses for a canonical bundle ref.
func TestBundleProfileRef_RoundTrip(t *testing.T) {
	const bundle = "https://github.com/o/r@bundles/code-review"
	ref, err := BundleProfileRef(bundle, "cr-correctness-rust")
	require.NoError(t, err)
	gotBundle, gotName, ok := SplitBundleProfileRef(ref)
	assert.True(t, ok)
	assert.Equal(t, "ctxloom+git://github.com/o/r//bundles/code-review", gotBundle)
	assert.Equal(t, "cr-correctness-rust", gotName)
}
