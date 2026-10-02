//go:build !windows

package safefs_test

import (
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// Only a regular file has bytes to lose. A device exists and reports size
// zero, so a guard that judged it would refuse every unwritten open of
// /dev/null and every empty rename near one.
func TestGuardFs_NonRegularFileIsNotJudged(t *testing.T) {
	f, err := safefs.NewGuardFs(afero.NewOsFs()).OpenFile(os.DevNull, os.O_WRONLY|os.O_TRUNC, 0)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}
