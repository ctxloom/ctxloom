//go:build integration

package integration

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// TestGenSchemasStub_WithoutTag_FailsLoud pins that `go run
// ./cmd/gen-schemas` without `-tags schemagen` used to hit the `!schemagen`
// stub whose entire body was `func main() {}` — exit 0, having generated
// nothing at all, silently. The stub must instead fail loud and point at the
// real invocation (`just gen-schemas`, which supplies the tag).
func TestGenSchemasStub_WithoutTag_FailsLoud(t *testing.T) {
	out, err := testenv.GoCombinedOutput(nil, "run", "github.com/ctxloom/ctxloom/cmd/gen-schemas")

	require.Error(t, err, "untagged gen-schemas stub must NOT exit 0; got output:\n%s", out)
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.NotEqual(t, 0, exitErr.ExitCode(), "stub must exit non-zero")
	assert.Contains(t, string(out), "gen-schemas", "stub stderr should name the gate")
	assert.Contains(t, string(out), "schemagen", "stub stderr should point at the schemagen tag or `just gen-schemas`")
}
