// Remote discovery tests verify that ctxloom can search for and identify remote
// repositories containing ctxloom content (fragments, prompts, bundles). This enables
// users to discover and install community-maintained AI context.
package remote

import (
	"context"
	"testing"
)

// =============================================================================
// Mock Fetcher Tests
// =============================================================================
// Mock fetcher enables testing discovery logic without network calls.

func TestMockFetcher_SearchRepos(t *testing.T) {
	// Mock fetcher returns nil for search - used to isolate tests from network
	fetcher := newMockFetcher()

	repos, err := fetcher.SearchRepos(context.Background(), "test", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repos != nil {
		t.Errorf("expected nil repos for mock, got %v", repos)
	}
}

// =============================================================================
// Repository Info Tests
// =============================================================================
// RepoInfo captures metadata for display and filtering of discovered repositories.

func TestRepoInfo_Fields(t *testing.T) {
	// All repository metadata must be accessible for filtering and display
	repo := RepoInfo{
		Owner:       "alice",
		Name:        "ctxloom",
		Description: "Test repo",
		Stars:       42,
		URL:         "https://github.com/alice/ctxloom",
		Topics:      []string{"golang", "security"},
		Language:    "Go",
		Forge:       ForgeGitHub,
	}

	if repo.Owner != "alice" {
		t.Errorf("Owner = %q, want %q", repo.Owner, "alice")
	}
	if repo.Name != "ctxloom" {
		t.Errorf("Name = %q, want %q", repo.Name, "ctxloom")
	}
	if repo.Stars != 42 {
		t.Errorf("Stars = %d, want %d", repo.Stars, 42)
	}
	if repo.Forge != ForgeGitHub {
		t.Errorf("Forge = %q, want %q", repo.Forge, ForgeGitHub)
	}
	if len(repo.Topics) != 2 {
		t.Errorf("Topics length = %d, want %d", len(repo.Topics), 2)
	}
}

func TestForgeType_Values(t *testing.T) {
	// Forge types must have stable string values for config and URL construction
	tests := []struct {
		name  string
		forge ForgeType
		want  string
	}{
		{
			name:  "github",
			forge: ForgeGitHub,
			want:  "github",
		},
		{
			name:  "gitlab",
			forge: ForgeGitLab,
			want:  "gitlab",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(tt.forge); got != tt.want {
				t.Errorf("ForgeType = %q, want %q", got, tt.want)
			}
		})
	}
}
