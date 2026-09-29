// Package parentwatch ties a process's lifetime to its parent's from the
// inside: WithParent returns a context that is cancelled when the process
// that spawned this one exits, however it exits.
//
// It exists for the platforms where the kernel will not do it for us. Linux
// arms PR_SET_PDEATHSIG at the spawn site (isolation's setRunnerPdeathsig),
// which also covers a foreign binary; darwin and the BSDs have no such
// attribute, so the child has to watch its parent itself.
package parentwatch
