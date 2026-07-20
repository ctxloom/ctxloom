package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// testBaseDir is the base directory used in tests across the operations package
const testBaseDir = "/project/" + paths.AppDirName

func TestGetFS(t *testing.T) {
	t.Run("returns provided fs", func(t *testing.T) {
		memFs := afero.NewMemMapFs()
		result := getFS(memFs)
		assert.Equal(t, memFs, result)
	})

	t.Run("returns OsFs when nil", func(t *testing.T) {
		result := getFS(nil)
		assert.NotNil(t, result)
	})
}
