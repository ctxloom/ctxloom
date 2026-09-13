package tmuxhost

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/enginenames"
)

// TestMain composes the lean engine-name root: engine aliases are populated
// by composition, never born into the table, and this binary links no
// descriptor — exactly ltk's and taskloom's situation, so it composes what
// they compose.
func TestMain(m *testing.M) {
	enginenames.MustRegister()
	os.Exit(m.Run())
}
