package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// TestArtifactStamper_RefusesAnEmptyFile pins that the publish path
// capped the MAXIMUM artifact size and never the minimum, so a 0-byte file
// uploaded, journaled, and came back as `{"journaled": true, "artifact_ids":
// [...]}`. An agent that published an empty plan got a success receipt for
// delivering nothing — this project's characteristic silent no-op, with a
// content-addressed id on top to make it look real.
//
// home is nil on purpose: the refusal must happen BEFORE the round trip, so
// reaching the upload at all would panic rather than quietly pass.
func TestArtifactStamper_RefusesAnEmptyFile(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "plan.md")
	require.NoError(t, os.WriteFile(empty, nil, 0o644))

	p := &artifactStamper{}
	got, err := p.publish(context.Background(), nil, artifactCandidate{
		artifactID: "plan/plan",
		name:       "plan.md",
		mediaType:  "text/markdown",
		kind:       agentcoordpb.ArtifactKind_ARTIFACT_KIND_IMPLEMENTATION_PLAN,
		absPath:    empty,
	})
	assert.Nil(t, got)
	require.Error(t, err, "a 0-byte artifact must not publish")
	assert.Contains(t, err.Error(), "empty")
	assert.Contains(t, err.Error(), empty, "the refusal must name the file so the agent can fix it")
}

// TestArtifactStamper_RefusesAWhitespaceOnlyFile: a file holding nothing but
// whitespace is the same delivery of nothing, one keystroke away. It carries
// no content for a reader and must not earn a receipt either.
func TestArtifactStamper_RefusesAWhitespaceOnlyFile(t *testing.T) {
	dir := t.TempDir()
	blank := filepath.Join(dir, "notes.plan.md")
	require.NoError(t, os.WriteFile(blank, []byte("\n\n   \t\n"), 0o644))

	p := &artifactStamper{}
	got, err := p.publish(context.Background(), nil, artifactCandidate{
		artifactID: "plan/notes",
		name:       "notes.plan.md",
		mediaType:  "text/markdown",
		absPath:    blank,
	})
	assert.Nil(t, got)
	require.Error(t, err, "a whitespace-only artifact must not publish")
	assert.Contains(t, err.Error(), "empty")
}
