//go:build linux

package procalive

import "github.com/ctxloom/ctxloom/internal/shared/procpin"

// isZombie reports whether procpin.ReadStat marks pid state Z.
//
// A read failure (the pid is already gone) is treated as "cannot tell, so not
// a zombie" rather than an error: Alive's kill(pid,0) check is what decides
// existence; this only narrows an already-confirmed-present pid.
func isZombie(pid int) bool {
	st, err := procpin.ReadStat(pid)
	return err == nil && st.State == 'Z'
}
