package remote

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
)

func TestLoadAuth_GitHubToken(t *testing.T) {
	t.Run("GITHUB_TOKEN", func(t *testing.T) {
		testsupport.Isolate(t)
		t.Setenv("GITHUB_TOKEN", "gh-token-123")

		auth := LoadAuth("")
		assert.Equal(t, "gh-token-123", auth.GitHub)
	})

	t.Run("GH_TOKEN", func(t *testing.T) {
		testsupport.Isolate(t)
		t.Setenv("GH_TOKEN", "gh-short-token")

		auth := LoadAuth("")
		assert.Equal(t, "gh-short-token", auth.GitHub)
	})

	t.Run("GH_TOKEN overrides GITHUB_TOKEN", func(t *testing.T) {
		testsupport.Isolate(t)
		t.Setenv("GITHUB_TOKEN", "first-token")
		t.Setenv("GH_TOKEN", "second-token")

		auth := LoadAuth("")
		// GH_TOKEN is checked after GITHUB_TOKEN, so it wins
		assert.Equal(t, "second-token", auth.GitHub)
	})
}

func TestLoadAuth_NoTokens(t *testing.T) {
	testsupport.Isolate(t)

	auth := LoadAuth("")
	assert.Empty(t, auth.GitHub)
}

