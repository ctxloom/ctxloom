package hostpty

import "github.com/aymanbagabas/go-pty"

// A ConPTY has no termios to make raw and no slave fd held by this process:
// the console host translates the master's VT input into console input
// records for the child itself, and the child is started on the pseudo
// console by attribute, not by inheriting a handle. Session leadership and a
// controlling terminal are Unix job-control concepts with no Windows
// counterpart.

func prepareSlave(pty.Pty) (release func(), err error) { return func() {}, nil }

func sessionAttrs(*pty.Cmd) {}
