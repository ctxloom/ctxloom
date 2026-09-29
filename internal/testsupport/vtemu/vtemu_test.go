package vtemu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A raw tty does no output processing: LF moves down and keeps the column.
// That is the staircase a caller assuming ONLCR paints, and the model must
// show it rather than hide it.
func TestScreen_LineFeedKeepsTheColumn(t *testing.T) {
	s := New(4, 20)
	s.Feed([]byte("ab\ncd\r\nef"))
	assert.Equal(t, "ab", s.Row(0))
	assert.Equal(t, "  cd", s.Row(1))
	assert.Equal(t, "ef", s.Row(2))
}

// Writing the last column leaves a wrap pending; only the next printable
// wraps, and a CR in between cancels it.
func TestScreen_AutowrapIsPendingUntilTheNextPrintable(t *testing.T) {
	s := New(3, 4)
	s.Feed([]byte("abcd"))
	r, c := s.Cursor()
	assert.Equal(t, [2]int{0, 3}, [2]int{r, c})
	s.Feed([]byte("\r\nxy"))
	assert.Equal(t, "xy", s.Row(1), "CR cancels the pending wrap: no blank line")
	s.Feed([]byte("zw!"))
	assert.Equal(t, "xyzw", s.Row(1))
	assert.Equal(t, "!", s.Row(2), "the fifth printable wraps")
}

// 1049 preserves the main screen and the cursor across a trip to the
// alternate screen, which starts blank.
func TestScreen_AltScreenRestoresTheMainScreenAndCursor(t *testing.T) {
	s := New(3, 10)
	s.Feed([]byte("main\x1b[2;3H"))
	s.Feed([]byte("\x1b[?1049h"))
	assert.True(t, s.OnAltScreen())
	assert.Equal(t, "", s.Row(0), "the alternate screen starts blank")
	s.Feed([]byte("\x1b[1;1Hpanel"))
	s.Feed([]byte("\x1b[?1049l"))
	assert.False(t, s.OnAltScreen())
	assert.Equal(t, "main", s.Row(0))
	r, c := s.Cursor()
	assert.Equal(t, [2]int{1, 2}, [2]int{r, c})
}

// A second 1049h while on the alternate screen saves into the alternate
// buffer's slot, so leaving still restores the cursor saved on entry.
func TestScreen_NestedAltEntryKeepsTheMainCursor(t *testing.T) {
	s := New(3, 10)
	s.Feed([]byte("\x1b[3;4H\x1b[?1049h\x1b[1;1H\x1b[?1049h\x1b[?1049l"))
	r, c := s.Cursor()
	assert.Equal(t, [2]int{2, 3}, [2]int{r, c})
}

func TestScreen_EditSequences(t *testing.T) {
	s := New(2, 10)
	s.Feed([]byte("abcdefgh\x1b[3G\x1b[2X"))
	assert.Equal(t, "ab  efgh", s.Row(0), "CHA then ECH")
	s.Feed([]byte("\x1b[2d\x1b[1GZ\x1b[3b"))
	assert.Equal(t, "ZZZZ", s.Row(1), "VPA, CHA, REP")
}

// An unmodeled sequence that may change cells is recorded, never guessed at;
// ones with no cell effect are not.
func TestScreen_RecordsWhatItDoesNotModel(t *testing.T) {
	s := New(2, 10)
	s.Feed([]byte("\x1b[1m\x1b[?2026$p\x1b[>4;2m\x1b[=1;1u\x1b[?25l"))
	assert.Empty(t, s.Unhandled())
	s.Feed([]byte("\x1b[4h\x1b[?69h"))
	assert.Equal(t, map[string]int{"CSI 4h": 1, "CSI ?69h": 1}, s.Unhandled())
}
