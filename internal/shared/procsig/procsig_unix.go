//go:build !windows

package procsig

import (
	"os"
	"syscall"
)

// Interrupt asks p to end what it is doing: SIGINT, to p alone.
func Interrupt(p *os.Process) error { return p.Signal(syscall.SIGINT) }

// Stop asks p to tear down in order: SIGTERM, which a runner's signal context
// turns into its own teardown.
func Stop(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// SpawnAttr starts the child as the leader of its own process group, so a
// signal aimed at the caller's group (a ^C on a terminal they share) never
// reaches it behind the caller's back.
func SpawnAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
