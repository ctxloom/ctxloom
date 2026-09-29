package containerprobe

import "os"

// SelfIDCandidates lists the ids this process's own container may carry,
// strongest first; nil when nothing suggests one (never the plain hostname of
// an uncontainerized host).
func SelfIDCandidates() []string { return SelfIDCandidatesFrom(os.ReadFile, os.Hostname) }
