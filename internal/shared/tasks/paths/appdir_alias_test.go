package paths

import (
	"testing"

	"github.com/stretchr/testify/assert"

	corepaths "github.com/ctxloom/ctxloom/internal/paths"
)

// U092-F11: the ctxloom dot-dir name used to be declared as an independent
// ".ctxloom" literal in BOTH this package and internal/paths, with no
// relationship. Consistent today, but a drift in either would silently stop
// projectroot.TaskStoreRoot's documented opt-out from matching the directory
// `ctxloom init` creates — a failure with no error at either end. AppDirName
// is now a const alias, so the compiler enforces the link; this pins the value
// and the alias against someone re-inlining the literal.
func TestAppDirName_IsTheCoreConstant(t *testing.T) {
	assert.Equal(t, corepaths.AppDirName, AppDirName)
	assert.Equal(t, ".ctxloom", AppDirName)
}
