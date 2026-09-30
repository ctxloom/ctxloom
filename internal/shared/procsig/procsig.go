// Package procsig is the ONE seam for asking a child process to stop. It is
// polymorphic by build tag, never by a GOOS branch: each OS file carries its
// own meaning of the three operations.
//
//   - Interrupt: the polite ask — end what you are doing and unwind. A
//     structured engine turn ends on it with its session intact, so the next
//     turn can resume it.
//   - Stop: the orderly-teardown request a supervised process (a runner) turns
//     into its own shutdown.
//   - SpawnAttr: the process attributes a child must be started with for
//     Interrupt to reach it, and ONLY it.
//
// Every one of them is a request the caller follows with a kill after its own
// grace (exec.Cmd.WaitDelay): nothing here guarantees the child complies.
package procsig
