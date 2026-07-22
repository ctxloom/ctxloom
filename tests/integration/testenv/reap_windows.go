//go:build windows

package testenv

// PluginChildrenOf and KillPids are no-ops on Windows for the same reason
// internal/lm/grpc's killSession is: no /proc, no cheap POSIX-style pid
// introspection or signal delivery without Job Objects (out of scope here).
// A hard-killed test session's orphaned plugin child is, today, the same
// pre-existing gap there as everywhere else on this platform — see
// internal/lm/grpc/procsession_windows.go for the identical precedent.
func PluginChildrenOf(ppid int) []int { return nil }
func KillPids(pids []int)             {}
