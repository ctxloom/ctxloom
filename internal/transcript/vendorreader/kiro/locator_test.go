package kiro

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLocator_RoundTripsThroughParseLocator pins the contract that makes the
// composite locator a locator at all: whatever Locator hands back, parseLocator
// must accept and take apart again into the same pair. Locator and parseLocator
// are inverses or they are nothing — a builder whose own reader rejects its
// output moves the failure to whoever finally calls Convert, a different
// subsystem with no idea which caller field was empty.
//
// The empty-component cases are the ones that used to break the round-trip:
// Locator concatenated them into "#conv-1" / "/tmp/db.sqlite3#" / "#", each of
// which parseLocator refuses. They are now refused at construction, so there is
// no such string to hand on.
func TestLocator_RoundTripsThroughParseLocator(t *testing.T) {
	ok := []struct{ dbPath, conversationID string }{
		{"/home/u/.local/share/kiro-cli/data.sqlite3", "74b27b3b-19e1-41f3-a8a6-bc8e227a3edb"},
		// A db path containing the separator: parseLocator splits on the LAST
		// one, so this round-trips too (kiro.go's locatorSep doc comment).
		{"/tmp/od#d/data.sqlite3", "conv-1"},
	}
	for _, tc := range ok {
		src, err := Locator(tc.dbPath, tc.conversationID)
		require.NoError(t, err)
		db, id, perr := parseLocator(src)
		require.NoError(t, perr, "Locator produced %q, which its own inverse rejects", src)
		require.Equal(t, tc.dbPath, db)
		require.Equal(t, tc.conversationID, id)
	}

	broken := []struct{ dbPath, conversationID string }{
		{"", "conv-1"},
		{"/tmp/data.sqlite3", ""},
		{"", ""},
	}
	for _, tc := range broken {
		src, err := Locator(tc.dbPath, tc.conversationID)
		require.Error(t, err,
			"Locator(%q, %q) built %q instead of refusing an empty component",
			tc.dbPath, tc.conversationID, src)
		require.Empty(t, src)
	}
}

// mustLocator is the test-side spelling of Locator for the many cases that
// pass a known-good pair and care only about the resulting src.
func mustLocator(t *testing.T, dbPath, conversationID string) string {
	t.Helper()
	src, err := Locator(dbPath, conversationID)
	require.NoError(t, err)
	return src
}
