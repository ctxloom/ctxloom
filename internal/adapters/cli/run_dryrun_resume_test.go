package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A --distill --dry-run must SHOW the essence the launch's SessionStart hook
// would inject, without distilling (a preview writes nothing). These cover the
// three states the harp's essence can be in.

func writeEssence(t *testing.T, _ /* home */, harp, body string) {
	t.Helper()
	essence, err := harpEssencePath(t, harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(essence, []byte(body), 0o644))
}

func TestDistilledResumePreview_ShowsWhatTheHookInjects(t *testing.T) {
	home := testsupport.Isolate(t)
	writeEssence(t, home, "swift-amber-falcon", "  the decision and why  \n")

	essence, note := distilledResumePreview("swift-amber-falcon", func(string) bool { return false })

	assert.Equal(t, resumedEssenceForInjection(1, "startup", "swift-amber-falcon", resumedPartsSession), essence,
		"the preview must be exactly what the SessionStart hook injects")
	assert.Equal(t, "the decision and why", essence)
	assert.Empty(t, note, "a current essence needs no caveat")
}

func TestDistilledResumePreview_MissingEssenceSaysTheLaunchDistills(t *testing.T) {
	testsupport.Isolate(t)
	staleAsked := false

	essence, note := distilledResumePreview("swift-amber-falcon", func(string) bool { staleAsked = true; return false })

	assert.Empty(t, essence)
	assert.Contains(t, note, "not distilled yet")
	assert.Contains(t, note, "distills it on demand")
	assert.False(t, staleAsked, "staleness is meaningless with no essence to compare")
}

func TestDistilledResumePreview_StaleEssenceIsShownWithTheCaveat(t *testing.T) {
	home := testsupport.Isolate(t)
	writeEssence(t, home, "swift-amber-falcon", "from before the clear")

	essence, note := distilledResumePreview("swift-amber-falcon", func(string) bool { return true })

	assert.Equal(t, "from before the clear", essence)
	assert.Contains(t, note, "stale")
	assert.Contains(t, note, "re-distills")
}
