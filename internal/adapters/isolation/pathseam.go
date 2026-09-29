package isolation

// pathSeam is the ONE place host↔container path translation lives: every
// mount this package binds is built here (bind/expose), and every argv source
// is named here (sourceFor), so no mount site can route a path its own way.
//
// It carries two rules that answer different questions about the same host
// path, and they stay apart inside it:
//
//   - target (pathMapper): where the container sees the path. It depends on
//     the host OS (a Windows drive letter lands under /mnt).
//   - source (sourceMapper): what the daemon calls the path. It depends on
//     where THIS process runs (docker-outside-of-docker names our path by the
//     daemon-side source of the mount covering it).
//
// Targets never read the source rule: under docker-outside-of-docker the
// source changes while the target must not, so ctxloom and the runner name
// every file by the same path. The source rule is applied only when a mount
// is rendered into argv (mountArgs), which is also where a path the daemon
// has no name for is refused (errNoDaemonSource).
//
// The zero value is valid: the host OS's target rule and the shared (identity)
// source rule — what a runtime that is not inside one of its daemon's
// containers uses.
type pathSeam struct {
	target pathMapper
	source sourceMapper
}

// newPathSeam builds the seam for a runtime: target nil is the host OS's
// mapper; self nil means this process shares the daemon's mount namespace.
func newPathSeam(target pathMapper, self *selfContainer) pathSeam {
	s := pathSeam{target: target, source: sharedSource{}}
	if target == nil {
		s.target = hostMapper()
	}
	if self != nil {
		s.source = selfMountSource{mounts: self.mounts}
	}
	return s
}

// targetFor is where the container sees host. It fails for a host path the
// runtime cannot mount at all.
func (s pathSeam) targetFor(host string) (string, error) {
	if s.target == nil {
		return hostMapper().toContainer(host)
	}
	return s.target.toContainer(host)
}

// sourceFor is the daemon's name for host: the bind SOURCE a rendered
// --mount carries. It fails for a path the daemon has no name for.
func (s pathSeam) sourceFor(host string) (string, error) {
	if s.source == nil {
		return host, nil
	}
	return s.source.toDaemon(host)
}

// expose binds host at the path the target rule routes it to. It fails
// exactly where targetFor does.
func (s pathSeam) expose(host string, readOnly bool) (mount, error) {
	target, err := s.targetFor(host)
	if err != nil {
		return mount{}, err
	}
	return s.bind(host, target, readOnly), nil
}

// bind binds host at a target the caller already decided: a container-anchored
// path (under the instance home, /probe), or a host path's routed target.
// mount.Host stays the path as THIS process sees it; sourceFor translates it
// at render.
func (s pathSeam) bind(host, target string, readOnly bool) mount {
	return mount{Host: host, Container: target, ReadOnly: readOnly}
}
