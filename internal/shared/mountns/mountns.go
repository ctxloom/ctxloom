// Package mountns answers, at runtime, one question — may this host give a
// process a bind-mounted file inside a private mount namespace? — and, when
// it can, performs that bind.
//
// Unprivileged user namespaces are a kernel- and policy-dependent privilege,
// not a property of the operating system, so the answer can never be inferred
// from GOOS. Hardened distributions disable them outright
// (kernel.unprivileged_userns_clone=0), and AppArmor can deny them per
// profile. The only trustworthy answer is to try it.
//
// Go cannot run code between clone(2) and execve(2) — there is no fork hook —
// so the mount cannot be performed by the spawning process. The standard
// answer, and the one container runtimes use, is to RE-EXEC OURSELVES:
// Command re-executes the current binary with instructions in its
// environment, and RunChildIfRequested is the entry point those instructions
// select. RunChildIfRequested must be called at the very top of main, before
// any other work, because the shim process must not do anything else.
//
// The shim execs the real argv, so it does not survive: there is no
// supervisor to orphan and no daemon to die silently. The mount is visible
// ONLY inside that namespace, nothing on the host sees it, and a crashed run
// leaves nothing behind — the namespace dies with its last process.
package mountns

import "errors"

// ErrUnsupported reports that this host refuses the namespace or the mount.
// Callers distinguish it with errors.Is: it is the difference between "this
// platform cannot do this" (select something else) and "this platform can and
// it went wrong" (a fault).
var ErrUnsupported = errors.New("mountns: this host does not permit an unprivileged user+mount namespace with a file bind mount")

// Bind is one file the shim binds inside the new namespace: Source (a host
// path) becomes visible AT Target, which must already exist as a file — a
// file bind mount mounts over an existing inode, it does not create one.
//
// This is deliberately NOT isolation's bind mount. That is a DESCRIPTOR handed to
// a container runtime, which performs it and reports its own failures; a Bind
// is an instruction this process carries out itself with mount(2), and fails
// as an errno inside our own shim before the engine is exec'd. The two share
// a kernel mechanism and nothing else.
type Bind struct {
	Source   string
	Target   string
	ReadOnly bool
}
