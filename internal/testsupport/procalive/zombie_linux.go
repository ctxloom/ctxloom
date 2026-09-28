//go:build linux

package procalive

import (
	"os"
	"strconv"
)

// isZombie reports whether pid's /proc/<pid>/stat marks it state Z.
//
// The second field (comm) is parenthesized and MAY ITSELF CONTAIN SPACES AND
// PARENTHESES — a process can be renamed via prctl(PR_SET_NAME) or argv[0]
// rewriting to almost anything, parens included — so the state character
// must be found from the LAST ')' in the line, never by splitting on
// whitespace or taking the first ')'. Either of those reads the wrong field
// whenever comm contains a ')' of its own; this is the classic parsing bug
// for this exact file.
//
// A read failure (the pid is already gone) is treated as "cannot tell, so not a zombie" rather than an error: Alive's
// kill(pid,0) check above is what decides existence; this only narrows an
// already-confirmed-present pid.
func isZombie(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == ')' {
			if i+2 < len(data) {
				return data[i+2] == 'Z'
			}
			return false
		}
	}
	return false
}
