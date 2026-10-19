//go:build windows

package ptyrunner

import "os"

// FreshLineDiscipline has nothing to set on a console: the console host gives
// a child it starts its own input mode, and a ConPTY carries no termios.
func FreshLineDiscipline(*os.File) (restore func() error, err error) {
	return func() error { return nil }, nil
}
