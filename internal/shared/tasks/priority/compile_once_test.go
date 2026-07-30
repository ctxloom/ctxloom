package priority

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
)

// Compilation happens ONCE per Compute call, ahead of the task loop -- not
// once per task. Measured on this tree, compiling the shipped default schema
// costs ~2.4ms and is ~22% of a 500-task Compute, so moving it inside the
// loop would be a 500x regression that no functional test would notice.
//
// Pinned structurally rather than by timing: with a formula that cannot
// compile, Compute must fail on an EMPTY task list. If compilation ever
// migrated into the per-task loop, zero tasks would mean zero compiles and
// the bad formula would come back green.
func TestCompute_CompilesBeforeTheTaskLoopNotPerTask(t *testing.T) {
	schema := mustSchema(t, `tagma.priority_fn:"triage:kind"="{{{{ not a valid expr (("`)

	_, _, err := Compute(nil, schema, fixedNow)
	require.Error(t, err, "an uncompilable formula must be caught with no tasks at all")
	assert.Contains(t, err.Error(), "triage:kind")

	_, _, err = Compute([]tasks.Task{}, schema, fixedNow)
	require.Error(t, err)
}
