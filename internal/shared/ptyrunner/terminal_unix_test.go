//go:build !windows

package ptyrunner

import (
	"os"
	"testing"

	"github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// slave opens a fresh pty and returns its slave, closed with the test.
func slave(t *testing.T) *os.File {
	t.Helper()
	p, err := pty.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	up, ok := p.(pty.UnixPty)
	require.True(t, ok)
	return up.Slave()
}

func termios(t *testing.T, f *os.File) *unix.Termios {
	t.Helper()
	got, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	require.NoError(t, err)
	return got
}

// RawInput turns off input processing — no echo, no line buffering, no CR
// rewriting, no signals from typed keys — and leaves output processing, so a
// plain "\n" still renders as a new line.
func TestRawInput_TurnsInputProcessingOffAndLeavesOutputAlone(t *testing.T) {
	s := slave(t)
	require.NoError(t, RawInput(s))
	got := termios(t, s)
	for name, bit := range map[string]uint64{"ICRNL": unix.ICRNL, "IXON": unix.IXON} {
		assert.Zero(t, uint64(got.Iflag)&bit, "%s is off", name)
	}
	for name, bit := range map[string]uint64{"ECHO": unix.ECHO, "ICANON": unix.ICANON, "ISIG": unix.ISIG, "IEXTEN": unix.IEXTEN} {
		assert.Zero(t, uint64(got.Lflag)&bit, "%s is off", name)
	}
	assert.NotZero(t, uint64(got.Oflag)&unix.OPOST, "output processing stays on")
	assert.NotZero(t, uint64(got.Oflag)&unix.ONLCR, "ONLCR stays on")
}

// FreshLineDiscipline gives a terminal whose input was made raw the line
// discipline a new pty starts with — what a program run on a terminal
// expects to find — and its restore puts back exactly what was there.
func TestFreshLineDiscipline_IsANewPtysAndItsRestorePutsTheOldOneBack(t *testing.T) {
	fresh := termios(t, slave(t))
	s := slave(t)
	require.NoError(t, RawInput(s))
	before := termios(t, s)

	restore, err := FreshLineDiscipline(s)
	require.NoError(t, err)
	got := termios(t, s)
	assert.Equal(t, fresh.Iflag, got.Iflag, "input flags are a new pty's")
	assert.Equal(t, fresh.Oflag, got.Oflag, "output flags are a new pty's")
	assert.Equal(t, fresh.Lflag, got.Lflag, "local flags are a new pty's")

	require.NoError(t, restore())
	assert.Equal(t, before, termios(t, s), "the restore puts back exactly the discipline that was there")
}
