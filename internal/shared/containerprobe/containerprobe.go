// Package containerprobe answers one question — is THIS process running
// inside a container? — for callers that cannot agree on a common home.
//
// It lives at the leaf because its two consumers sit on opposite sides of an
// import cycle: internal/lm/isolation owns the launch decision, while
// internal/lm/backends' mock records the answer as evidence of WHERE an engine
// ran, and backends cannot import isolation (isolation -> backends -> acp ->
// isolation). A second copy of the marker list is exactly the drift this
// package exists to prevent: the copies would answer differently the first
// time a runtime changed its sentinel, and the disagreement would surface as a
// test that quietly stopped proving containment.
package containerprobe

import (
	"os"
	"strings"
)

// InContainer reports whether this process is running inside a container
// (dev container, CI job, pod).
//
// It is a HEURISTIC and is documented as one everywhere it is consumed: it
// gates devcontainer-specific wording and feeds diagnostics, but the
// can-containers-launch decision is behavior-based (runtime reachability plus
// the shared-filesystem probe), never this alone.
func InContainer() bool { return len(Markers()) > 0 }

// Markers returns every in-container marker that matched, named for
// diagnostics. Empty means none matched.
func Markers() []string {
	return MarkersFrom(
		func(p string) error { _, err := os.Stat(p); return err },
		os.ReadFile,
		os.Getenv,
	)
}

// MarkersFrom is the seam-injected core of the detection: stat/readFile/getenv
// arrive as functions so tests never touch the real /proc, the sentinel files,
// or the process environment. Both are load-bearing here — CI itself runs
// inside containers, and the hostile-env suite junks the environment.
func MarkersFrom(stat func(string) error, readFile func(string) ([]byte, error), getenv func(string) string) []string {
	var markers []string
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if stat(f) == nil {
			markers = append(markers, f)
		}
	}
	for _, e := range []string{"REMOTE_CONTAINERS", "CODESPACES", "DEVCONTAINER", "KUBERNETES_SERVICE_HOST"} {
		if getenv(e) != "" {
			markers = append(markers, "$"+e)
		}
	}
	// cgroup v1 runtime signatures — BEST-EFFORT ONLY: cgroup v2 exposes a
	// bare "0::/" with no runtime marker, which is why the sentinel-file and
	// env probes lead and this never stands alone as a negative signal.
	if b, err := readFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		for _, marker := range []string{"docker", "containerd", "kubepods", "/lxc/"} {
			if strings.Contains(s, marker) {
				markers = append(markers, "cgroup:"+marker)
			}
		}
	}
	return markers
}
