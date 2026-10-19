//go:build !windows

package ptyrunner

import (
	"os"

	"golang.org/x/sys/unix"
)

// RawInput turns off tty's INPUT processing: no echo, no line buffering, no
// CR rewritten to LF, and no signal raised by a typed key, so every byte
// written to the master reaches whoever reads the slave as it was written.
// Output processing is left alone: a plain "\n" written to the slave still
// renders as a new line (ONLCR).
func RawInput(tty *os.File) error {
	fd := int(tty.Fd())
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return err
	}
	t.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	t.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(fd, ioctlSetTermios, t)
}

// FreshLineDiscipline gives tty the line discipline a new pty starts with —
// canonical, echoing input with CR read as LF and ^C a signal, and output
// with ONLCR — whatever mode it is in now, and returns the restore of the
// mode it found. A program started on a terminal expects to find it so, and
// sets any other mode it wants itself.
func FreshLineDiscipline(tty *os.File) (restore func() error, err error) {
	fd := int(tty.Fd())
	was, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	t := *was
	t.Iflag = unix.ICRNL | unix.IXON
	t.Oflag = unix.OPOST | unix.ONLCR
	t.Lflag = unix.ISIG | unix.ICANON | unix.ECHO | unix.ECHOE | unix.ECHOK | unix.ECHOCTL | unix.ECHOKE | unix.IEXTEN
	t.Cflag = t.Cflag&^unix.CSIZE | unix.CS8 | unix.CREAD
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &t); err != nil {
		return nil, err
	}
	return func() error { return unix.IoctlSetTermios(fd, ioctlSetTermios, was) }, nil
}
