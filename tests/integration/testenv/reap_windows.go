//go:build windows

package testenv

// RunnerChildrenOf and KillPids are no-ops on Windows for the same reason
// internal/adapters/isolation's killSession is: no /proc, no cheap
// POSIX-style pid introspection or signal delivery without Job Objects (out
// of scope here). A hard-killed test session's orphaned runner child is the
// same gap here as on the production side (isolation's
// procsession_windows.go).
func RunnerChildrenOf(ppid int) []int { return nil }
func KillPids(pids []int)             {}
