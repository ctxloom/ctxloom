package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// U019-F08 claimed the upload's artifact_id "is never used to store, key or
// resolve anything" and is therefore gratuitous coupling. Refuted: it is the
// only thing that correlates a blob receipt with the manifest that arrives
// separately.
//
// The blob is keyed by content hash and the manifest rides a later
// ArtifactProduced plane-1 event, so artifact_id is precisely what ties the
// two halves of one produce together — in the audit journal at upload time,
// and as the RESOLUTION key on the download side (artifactRecord(harp,
// artifact_id)). Drop the requirement and an upload becomes an anonymous
// blob with no auditable link to the artifact it belongs to; two children
// racing the same content would be indistinguishable in the trail.
//
// Pinned, not just noted: these go red if the requirement or the audit field
// is ever removed.
func TestArtifactUpload_ArtifactIDIsTheAuditCorrelation(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	data := []byte("content whose hash says nothing about which artifact it is")
	sum := sha256.Sum256(data)
	receipt, err := child.UploadArtifact(context.Background(), "plan/rollout", "rollout.plan.md", "text/markdown", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)

	entries := readAuditKind(t, c, "artifact.uploaded")
	require.Len(t, entries, 1)
	assert.Equal(t, "plan/rollout", entries[0].Detail["artifact_id"],
		"the audit trail's only link from this blob to the artifact it belongs to")
	assert.Equal(t, receipt.GetUploadId(), entries[0].Detail["sha256"])
}

// The requirement itself: an empty artifact_id is refused before any bytes
// are read, so an unattributable blob cannot be created at all.
func TestArtifactUpload_EmptyArtifactIDRefused(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	data := []byte("an upload that names no artifact")
	sum := sha256.Sum256(data)
	_, err := uploadRaw(t, client, out.RunID, "", data, sum[:], 0)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, statusCode(err))
	assert.Contains(t, err.Error(), "artifact_id is required")
}
