package grpc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The refusal message IS the entire user interface for this failure, so a
// confidently wrong one is worse than none. The old message asserted from an
// EQUALITY test that the daemon "predates this install" and told the reader to
// stop that process. When this actually fired the opposite was true — the
// daemon was newer and the caller's own long-lived process was stale — so the
// advice pointed at a process hosting live sessions.
//
// These pin the direction claim to what the stamps actually establish.
func TestVersionMismatchDetail_DoesNotClaimAnUnearnedDirection(t *testing.T) {
	const (
		older  = "v0.7.0-aaaaaaa-20260901T120000"
		newer  = "v0.7.0-bbbbbbb-20260908T120000"
		unstmp = "some-unstamped-build"
	)

	t.Run("daemon NEWER than client must not be accused of predating it", func(t *testing.T) {
		got := versionMismatchDetail(newer, older)

		require.Contains(t, got, "THIS CLIENT is the older build",
			"with a newer daemon the client is the stale side and the message must say so")
		require.Contains(t, got, "killing it would not help",
			"the remedy must not send the reader to kill the daemon that is current")
		// The specific falsehood this task exists to remove.
		require.NotContains(t, got, "earlier install",
			"an earlier-install claim about the daemon is false when the daemon is newer")
	})

	t.Run("daemon OLDER than client names the daemon, which is the earned claim", func(t *testing.T) {
		got := versionMismatchDetail(older, newer)
		require.Contains(t, got, "The DAEMON is the older build")
		require.Contains(t, got, "earlier install",
			"here the earlier-install claim IS established by the stamps")
	})

	t.Run("an unparseable stamp ranks neither side", func(t *testing.T) {
		for _, pair := range [][2]string{{unstmp, newer}, {newer, unstmp}} {
			got := versionMismatchDetail(pair[0], pair[1])
			require.Contains(t, got, "cannot be determined")
			require.NotContains(t, got, "older build",
				"an order must not be asserted when a stamp does not parse")
		}
	})

	t.Run("every arm names both stamps and gives a followable remedy", func(t *testing.T) {
		for _, pair := range [][2]string{{newer, older}, {older, newer}, {unstmp, newer}} {
			got := versionMismatchDetail(pair[0], pair[1])
			require.Contains(t, got, pair[0], "the daemon's stamp must appear")
			require.Contains(t, got, pair[1], "the client's stamp must appear")
			require.Contains(t, got, "retry")
			// Naming a concrete process to kill is what made the old message harmful.
			require.False(t, strings.Contains(got, "stop the stale"),
				"the message must not order the reader to stop a named process")
		}
	})
}
