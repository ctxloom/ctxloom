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
// CHARACTERIZATION, pre-fix: the empty-component cases below are the ones the
// round-trip currently FAILS, and this test records that. It is inverted (into
// an assertion that Locator refuses those inputs outright) by the fix.
func TestLocator_RoundTripsThroughParseLocator(t *testing.T) {
	ok := []struct{ dbPath, conversationID string }{
		{"/home/u/.local/share/kiro-cli/data.sqlite3", "74b27b3b-19e1-41f3-a8a6-bc8e227a3edb"},
		// A db path containing the separator: parseLocator splits on the LAST
		// one, so this round-trips too (kiro.go's locatorSep doc comment).
		{"/tmp/od#d/data.sqlite3", "conv-1"},
	}
	for _, tc := range ok {
		src := Locator(tc.dbPath, tc.conversationID)
		db, id, err := parseLocator(src)
		require.NoError(t, err, "Locator produced %q, which its own inverse rejects", src)
		require.Equal(t, tc.dbPath, db)
		require.Equal(t, tc.conversationID, id)
	}

	broken := []struct{ dbPath, conversationID string }{
		{"", "conv-1"},
		{"/tmp/data.sqlite3", ""},
		{"", ""},
	}
	for _, tc := range broken {
		src := Locator(tc.dbPath, tc.conversationID)
		_, _, err := parseLocator(src)
		require.Error(t, err,
			"Locator(%q, %q) produced %q, which parseLocator accepts — this characterization is stale",
			tc.dbPath, tc.conversationID, src)
	}
}
