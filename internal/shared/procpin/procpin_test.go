package procpin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// comm may contain spaces and parentheses; every field after it is found from
// the LAST ')', so a hostile process name cannot shift ppid or tty_nr.
func TestParseStat_CommWithParensAndSpaces(t *testing.T) {
	line := "4242 (evil) S 1 2 (x) S 99 7 4242 34816 4242 0 0 0 0 0 0 0 0 0 20 0 1 0 987654 0 0\n"
	st, err := parseStat([]byte(line))
	require.NoError(t, err)
	assert.Equal(t, Stat{State: 'S', PPID: 99, Session: 4242, TTYNr: 34816, StartTicks: 987654}, st)
}

func TestParseStat_RefusesTruncatedLine(t *testing.T) {
	_, err := parseStat([]byte("4242 (x) S 1 2 3"))
	require.Error(t, err)
}
