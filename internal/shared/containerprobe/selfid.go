package containerprobe

import (
	"regexp"
	"strings"
)

// selfIDSources are the per-runtime traces of a container's own id, strongest
// first. Each pattern's first group is the id.
//
//   - mountinfo: docker bind-mounts /etc/hostname|hosts|resolv.conf from
//     <data-root>/containers/<id>/ (rootful and rootless alike), podman from
//     …/overlay-containers/<id>/userdata/. It is the source that survives
//     cgroup v2 with a private cgroup namespace, where /proc/self/cgroup is
//     a bare "0::/".
//   - /run/.containerenv: podman writes the id there.
//   - cgroup v1: …/docker/<id>, docker-<id>.scope, libpod-<id>.scope.
var selfIDSources = []struct {
	path    string
	pattern *regexp.Regexp
}{
	{"/proc/self/mountinfo", regexp.MustCompile(`/(?:overlay-)?containers/([0-9a-f]{64})/`)},
	{"/run/.containerenv", regexp.MustCompile(`(?m)^id="([0-9a-f]{64})"`)},
	{"/proc/self/cgroup", regexp.MustCompile(`(?:/docker/|docker-|libpod-)([0-9a-f]{64})`)},
}

// shortIDHostname is docker's default container hostname: the short id (12
// hex) or, set explicitly, up to the full 64.
var shortIDHostname = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// SelfIDCandidatesFrom is SelfIDCandidates' seam-injected core. It only
// PROPOSES ids: a candidate is this process's container only once the daemon
// driving the run confirms it, which is the caller's job. A plain hostname is
// never proposed, so an uncontainerized host yields nil and costs its caller
// no daemon call.
func SelfIDCandidatesFrom(readFile func(string) ([]byte, error), hostname func() (string, error)) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, src := range selfIDSources {
		b, err := readFile(src.path)
		if err != nil {
			continue
		}
		for _, m := range src.pattern.FindAllStringSubmatch(string(b), -1) {
			add(m[1])
		}
	}
	if h, err := hostname(); err == nil && shortIDHostname.MatchString(strings.TrimSpace(h)) {
		add(strings.TrimSpace(h))
	}
	return out
}
