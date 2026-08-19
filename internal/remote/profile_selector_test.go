package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProfileSelector pins the "<bundle>#profiles/<name>" grammar — the profile
// counterpart to FragmentSelector ("#fragments/") and CommandSelector
// ("#commands/") — so bundle profiles are addressed consistently with the other
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
	tests := []struct {
		name   string
		ref    string
		want   string
		wantOK bool
	}{
		{
			name:   "plain bundle-profile ref is already canonical",
			ref:    repo + "@bundles/default#profiles/default",
			want:   repo + "@bundles/default#profiles/default",
			wantOK: true,
		},
		{
			name:   "bundle-part version pin dropped",
			ref:    repo + "@bundles/default@abc1234#profiles/default",
			want:   repo + "@bundles/default#profiles/default",
			wantOK: true,
		},
		{
			name:   "name-trailing version pin dropped",
			ref:    repo + "@bundles/default#profiles/default@abc1234",
			want:   repo + "@bundles/default#profiles/default",
			wantOK: true,
		},
		{
			name:   "local bundle profile canonicalizes",
			ref:    "code-review#profiles/base",
			want:   "ctxloom:local@bundles/code-review#profiles/base",
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

// TestSplitRetiredProfileRef pins recognition of the RETIRED top-level profile
// distribution grammar ("<url>@profiles/<name>"): the successor bundle-profile
// form, local names, and ctxloom:local refs must never match, and a trailing
// "@<version>" pin is dropped (the successor grammar pins via the bundle's
// lockfile entry).
func TestSplitRetiredProfileRef(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		wantURL  string
		wantName string
		wantOK   bool
	}{
		{
			name:     "retired https ref splits",
			ref:      "https://github.com/ctxloom/ctxloom-default@profiles/go-developer",
			wantURL:  "https://github.com/ctxloom/ctxloom-default",
			wantName: "go-developer",
			wantOK:   true,
		},
		{
			name:     "version pin is dropped",
			ref:      "https://github.com/o/r@profiles/dev@abc1234",
			wantURL:  "https://github.com/o/r",
			wantName: "dev",
			wantOK:   true,
		},
		{
			name:     "ssh ref splits despite the scheme '@'",
			ref:      "git@github.com:o/r@profiles/dev",
			wantURL:  "git@github.com:o/r",
			wantName: "dev",
			wantOK:   true,
		},
		{
			name:   "successor bundle-profile form is NOT retired",
			ref:    "https://github.com/o/r@bundles/kit#profiles/dev",
			wantOK: false,
		},
		{
			name:   "canonical bundle ref is NOT retired",
			ref:    "https://github.com/o/r@bundles/kit",
			wantOK: false,
		},
		{
			name:   "local profile name is NOT retired",
			ref:    "go-developer",
			wantOK: false,
		},
		{
			name:   "ctxloom:local ref is NOT retired",
			ref:    "ctxloom:local@profiles/dev",
			wantOK: false,
		},
		{
			name:   "empty profile name does not match",
			ref:    "https://github.com/o/r@profiles/",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, name, ok := SplitRetiredProfileRef(tt.ref)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantURL, url)
				assert.Equal(t, tt.wantName, name)
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
	assert.Equal(t, bundle, gotBundle)
	assert.Equal(t, "cr-correctness-rust", gotName)
}
