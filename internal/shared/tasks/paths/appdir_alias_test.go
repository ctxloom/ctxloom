package paths

import (
	"testing"

	"github.com/stretchr/testify/assert"

	corepaths "github.com/ctxloom/ctxloom/internal/paths"
)

// The ctxloom dot-dir name is a const ALIAS of internal/paths.AppDirName, not
// an independent ".ctxloom" literal declared in this package too. Two unrelated
// literals stay consistent right up until one drifts, at which point
// projectroot.TaskStoreRoot's documented opt-out silently stops matching the
// directory `ctxloom init` creates — a failure with no error at either end. The
// alias makes the compiler enforce the link; this pins the value and the alias
// against someone re-inlining the literal.
func TestAppDirName_IsTheCoreConstant(t *testing.T) {
	assert.Equal(t, corepaths.AppDirName, AppDirName)
	assert.Equal(t, ".ctxloom", AppDirName)
}
