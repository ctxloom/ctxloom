//go:build !linux && !darwin && !windows

package procalive

// isZombie cannot read a process state here, so it answers "cannot tell,
// so not a zombie" and Alive degrades to kill(pid, 0) alone.
func isZombie(int) bool { return false }
