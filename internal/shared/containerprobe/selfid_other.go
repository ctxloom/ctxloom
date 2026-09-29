//go:build !linux

package containerprobe

// SelfIDCandidates is nil off Linux: a native macOS or Windows process is
// never a container its daemon runs, so it has no id to propose.
func SelfIDCandidates() []string { return nil }
