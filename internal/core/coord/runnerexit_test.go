package coord

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// classifiedExit stands in for the adapter's classified exit
// (exitstatus.Error, which core cannot import): the reporting error's text
// and chain, plus the status coord reads through ExitStatus().
type classifiedExit struct {
	code int
	err  error
}

func (e classifiedExit) Error() string   { return e.err.Error() }
func (e classifiedExit) Unwrap() error   { return e.err }
func (e classifiedExit) ExitStatus() int { return e.code }

// TestExitCodeOf_ReadsTheAdaptersClassification: a Wait error carrying a
// classified status (anywhere in its chain) yields that status; nil is 0; an
// error with no classified status yields no code at all.
func TestExitCodeOf_ReadsTheAdaptersClassification(t *testing.T) {
	code := exitCodeOf(fmt.Errorf("host runner exited: %w", classifiedExit{code: 137, err: errors.New("signal: killed")}))
	require.NotNil(t, code)
	assert.Equal(t, int32(137), *code)

	zero := exitCodeOf(nil)
	require.NotNil(t, zero)
	assert.Equal(t, int32(0), *zero)

	assert.Nil(t, exitCodeOf(errors.New("wait: no child processes")))
}
