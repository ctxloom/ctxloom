package lockwait

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// WaitLine is the whole line the watchdog warns for label under this test
// binary's own name, without its newline, so the test compares the exact line
// rather than a substring.
func WaitLine(label string) string {
	return strings.TrimSuffix(clidiag.Line(filepath.Base(os.Args[0]), "%s", waitNotice(label)), "\n")
}
