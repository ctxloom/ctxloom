package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// TestOneShot_EmptyStdoutIsLoud pins that a turn used to accept exit 0 with
// nothing on stdout as a successful run, publishing "" as the answer with no
// error anywhere. A one-shot exists ONLY to capture output, so zero bytes is
// a failed turn, not a legitimately-empty one.
func TestOneShot_EmptyStdoutIsLoud(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)

	cases := []struct{ name, out string }{
		{"no bytes at all", ""},
		{"whitespace only", "  \n\t\n "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubClient{out: tc.out}
			o, err := testOneShot(t, cfg, opPipe(cfg, loader), stub, launch.Source{Profiles: []string{"rev"}})
			require.NoError(t, err)
			out, err := o.Turn(context.Background(), "review this diff")
			require.Error(t, err, "exit 0 with empty stdout must fail loudly, not report a successful empty run")
			assert.Empty(t, out)
			assert.Contains(t, err.Error(), "no output")
		})
	}
}
