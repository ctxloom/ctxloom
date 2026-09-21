//go:build windows

package isolation

import "os/exec"

// isolateRunner is a no-op on Windows: syscall.SysProcAttr has no Setsid field
// there, and Windows has no POSIX-style session/process-group kill
// primitive — the equivalent would be a Job Object, out of scope here.
func isolateRunner(_ *exec.Cmd) {}

// killSession is a no-op on Windows for the same reason isolateRunner is: no cheap
// unix-style session kill without Job Objects. A hard-killed host runner's
// orphaned grandchild is the honest gap there.
func killSession(_ int) {}
