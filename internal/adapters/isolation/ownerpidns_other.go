//go:build !linux

package isolation

// ownerPIDNamespace is "" off Linux: a native macOS or Windows ctxloom has one
// pid namespace, and it is never another container's — so its owner-pids
// compare among themselves, and never with a Linux container's.
func ownerPIDNamespace() string { return "" }
