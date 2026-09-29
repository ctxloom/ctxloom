package isolation

import "os"

// ownerPIDNamespace names the pid namespace this process's pid is read in —
// the kernel's own name for it ("pid:[4026531836]"); "" when unreadable.
func ownerPIDNamespace() string {
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return ""
	}
	return ns
}
