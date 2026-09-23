package hostpty

import (
	"os"
	"syscall"

	"github.com/creack/pty"
)

// Windows has no Unix pty: creack/pty's Open refuses with pty.ErrUnsupported
// before either of these is reached, and they return that same sentinel rather
// than guess at a console equivalent.

func sessionAttrs() *syscall.SysProcAttr { return nil }

func rawInput(*os.File) error { return pty.ErrUnsupported }
