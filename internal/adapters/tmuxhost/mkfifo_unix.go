//go:build !windows

package tmuxhost

import "golang.org/x/sys/unix"

// mkfifo makes the named pipe a launcher's environment travels through.
func mkfifo(path string, mode uint32) error { return unix.Mkfifo(path, mode) }
