package allowedsigners

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- usage-demonstrating: this is how principals/namespaces patterns behave ---

func TestMatchPatternList_LiteralMatch(t *testing.T) {
	assert.True(t, matchPatternList([]string{"ben@abbitt.me"}, "ben@abbitt.me"))
	assert.False(t, matchPatternList([]string{"ben@abbitt.me"}, "someone-else@abbitt.me"))
}

func TestMatchPatternList_MultipleEntriesAnyMatches(t *testing.T) {
	list := []string{"alice@example.com", "bob@example.com"}
	assert.True(t, matchPatternList(list, "alice@example.com"))
	assert.True(t, matchPatternList(list, "bob@example.com"))
	assert.False(t, matchPatternList(list, "carol@example.com"))
}

func TestMatchPatternList_GlobStar(t *testing.T) {
	list := []string{"*@example.com"}
	assert.True(t, matchPatternList(list, "anyone@example.com"))
	assert.True(t, matchPatternList(list, "@example.com")) // star matches zero chars
	assert.False(t, matchPatternList(list, "anyone@example.org"))
}

func TestMatchPatternList_GlobQuestionMark(t *testing.T) {
	list := []string{"user?@example.com"}
	assert.True(t, matchPatternList(list, "user1@example.com"))
	assert.False(t, matchPatternList(list, "user12@example.com")) // ? is exactly one char
	assert.False(t, matchPatternList(list, "user@example.com"))   // ? requires one char present
}

// --- edge cases ---

func TestMatchPatternList_Negation_ExcludesEvenIfOtherPatternMatches(t *testing.T) {
	// ssh_config(5) PATTERNS: a negated match disqualifies the whole list
	// regardless of any positive match elsewhere in the list.
	list := []string{"*@example.com", "!bob@example.com"}
	assert.True(t, matchPatternList(list, "alice@example.com"))
	assert.False(t, matchPatternList(list, "bob@example.com"))
}

func TestMatchPatternList_EmptyListMatchesNothing(t *testing.T) {
	assert.False(t, matchPatternList(nil, "anything"))
	assert.False(t, matchPatternList([]string{}, "anything"))
}

func TestMatchPatternList_CaseSensitive(t *testing.T) {
	assert.False(t, matchPatternList([]string{"Ben@Abbitt.Me"}, "ben@abbitt.me"))
}

func TestGlobMatch_StarCollapsesConsecutive(t *testing.T) {
	assert.True(t, globMatchBytes([]byte("**"), []byte("anything")))
	assert.True(t, globMatchBytes([]byte("a**b"), []byte("aXXXb")))
}

func TestGlobMatch_NoWildcardsRequiresExactMatch(t *testing.T) {
	assert.True(t, globMatchBytes([]byte("publish.v1.ctxloom.dev"), []byte("publish.v1.ctxloom.dev")))
	assert.False(t, globMatchBytes([]byte("publish.v1.ctxloom.dev"), []byte("publish.v1.ctxloom.devX")))
	assert.False(t, globMatchBytes([]byte("publish.v1.ctxloom.dev"), []byte("Xpublish.v1.ctxloom.dev")))
}

// --- timestamp parsing (valid-after / valid-before values) ---

func TestParseTimestamp_DateOnlyUTC(t *testing.T) {
	got, err := parseTimestamp("20200101Z")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), got)
}

func TestParseTimestamp_DateTimeWithSecondsUTC(t *testing.T) {
	got, err := parseTimestamp("20211231235959Z")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2021, 12, 31, 23, 59, 59, 0, time.UTC), got)
}

func TestParseTimestamp_DateTimeWithoutSecondsUTC(t *testing.T) {
	got, err := parseTimestamp("202101011200Z")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2021, 1, 1, 12, 0, 0, 0, time.UTC), got)
}

func TestParseTimestamp_NoZUsesLocalTimeZone(t *testing.T) {
	got, err := parseTimestamp("20200101")
	require.NoError(t, err)
	want := time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local)
	assert.True(t, got.Equal(want), "got %v want %v", got, want)
	assert.Equal(t, time.Local, got.Location())
}

func TestParseTimestamp_Invalid(t *testing.T) {
	_, err := parseTimestamp("not-a-timestamp")
	assert.Error(t, err)

	_, err = parseTimestamp("2020010") // 7 digits, not a valid length
	assert.Error(t, err)

	_, err = parseTimestamp("20201301Z") // month 13
	assert.Error(t, err)
}

// TestGlobMatchBytes_PathologicalPatternDoesNotBacktrack pins the matcher's
// running time on the input shape its structure invites.
//
// The recursive form tried every split point for every '*' independently, so a
// pattern of n stars separated by literals took exponential time in n against a
// non-matching subject. The consecutive-star collapse only merged ADJACENT
// stars, which does nothing for *a*a*a*a*b. Both operands are attacker-shaped
// in the TrustedAs path — the pattern comes from the trust root's principals
// field, the subject from an externally claimed identity (a git committer
// email, a loadout envelope's advisory signer field).
//
// Measured on the recursive form: 11 stars against 40 'a's ran for more than
// 130 seconds. The size here is deliberately one step down from that, so a
// regression costs the suite a second or two rather than hanging it.
func TestGlobMatchBytes_PathologicalPatternDoesNotBacktrack(t *testing.T) {
	pattern := []byte(strings.Repeat("*a", 10) + "*b")
	subject := []byte(strings.Repeat("a", 40))

	start := time.Now()
	got := globMatchBytes(pattern, subject)
	elapsed := time.Since(start)

	assert.False(t, got, "no 'b' in the subject, so this cannot match")
	t.Logf("elapsed: %s", elapsed)
	assert.Less(t, elapsed, 100*time.Millisecond, "matching must not backtrack exponentially")
}
