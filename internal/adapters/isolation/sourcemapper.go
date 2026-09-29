package isolation

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// sourceMapper names, on the daemon's filesystem, the file this process sees
// at p: the bind SOURCE, where pathMapper decides the TARGET. The two are
// separate seams on purpose — under docker-outside-of-docker the source must
// change while the target must not, so the runner names every file by the same
// path ctxloom does.
type sourceMapper interface {
	toDaemon(p string) (string, error)
}

// sharedSource is identity: the daemon shares this process's mount namespace
// (ctxloom on the daemon's host, or in a container the daemon does not run).
type sharedSource struct{}

func (sharedSource) toDaemon(p string) (string, error) { return p, nil }

// selfMountSource translates through self's own mounts: the daemon's path for
// p is the source of the longest mount destination covering p, plus the rest
// of p. Volumes count (their source is the daemon-side data dir a bind can
// name); anything else — a tmpfs, the container's own rootfs — has no name on
// the daemon and is refused.
type selfMountSource struct{ mounts []selfMount }

// errNoDaemonSource refuses a bind source no mount of self's covers.
var errNoDaemonSource = errors.New("isolation: no mount the daemon gave this process's container covers this path")

func (s selfMountSource) toDaemon(p string) (string, error) {
	p = path.Clean(p)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	best, bestLen := -1, -1
	for i, m := range s.mounts {
		d := path.Clean(m.destination)
		if (p == d || strings.HasPrefix(p, strings.TrimSuffix(d, "/")+"/")) && len(d) > bestLen {
			best, bestLen = i, len(d)
		}
	}
	if best < 0 {
		return "", fmt.Errorf("%w: %s", errNoDaemonSource, p)
	}
	m := s.mounts[best]
	return path.Join(m.source, strings.TrimPrefix(p, path.Clean(m.destination))), nil
}

// sources returns this runtime's bind-source translation: through self's
// mounts when this process is one of the daemon's containers, else identity.
func (rt ociRuntime) sources() sourceMapper {
	if rt.self == nil {
		return sharedSource{}
	}
	return selfMountSource{mounts: rt.self.mounts}
}

// daemonSourceRemedy is the fix for a path the daemon has no name for.
const daemonSourceRemedy = "put it on a bind mount or volume of ctxloom's own container (a devcontainer typically needs ~/.ctxloom on a volume), or run ctxloom on the daemon's host"
