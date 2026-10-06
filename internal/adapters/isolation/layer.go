package isolation

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// LayerMount is one directory visible in a layer: Host is its path in the
// daemon's (host) path space, View is where this layer sees it.
type LayerMount struct {
	Host, View string
	ReadOnly   bool
}

// Layer is one filesystem view of the host: the PRIMARY (this controller
// process) or a CHILD (a container it launches). Every path crossing between
// two layers goes through the host: out of one by Reverse, into the other by
// Map. The daemon resolves a bind source in host path space, so a source is
// always a Reverse result, never a view.
type Layer struct {
	mounts []LayerMount
	// shared: this layer IS the host namespace, so both directions are
	// identity.
	shared bool
	// ownFS: the views are this process's own paths, so Reverse resolves a
	// symlink before translating — the daemon must bind what the link names.
	ownFS bool
}

// ErrUnmapped refuses a path no mount of the layer covers: it has no name on
// the other side.
var ErrUnmapped = errors.New("isolation: path is not under any mount of this layer")

// HostLayer is the host's own view: a process that shares the daemon's mount
// namespace (ctxloom on the daemon's host, or in a container the daemon does
// not run).
func HostLayer() Layer { return Layer{shared: true} }

// primaryLayer is this process's layer: the host layer when it is not one of
// the daemon's containers, else self's daemon-reported mounts. A tmpfs or the
// container's own rootfs is absent from them (decodeSelf), so a path there is
// ErrUnmapped.
func primaryLayer(self *selfContainer) Layer {
	if self == nil {
		return HostLayer()
	}
	l := Layer{ownFS: true}
	for _, m := range self.mounts {
		l.mounts = append(l.mounts, LayerMount{Host: m.source, View: m.destination})
	}
	return l
}

// Reverse names view in host path space: the Host of the longest mount View
// covering it, plus the rest of view.
func (l Layer) Reverse(view string) (string, error) {
	if l.shared {
		return view, nil
	}
	view = path.Clean(view)
	if l.ownFS {
		if real, err := filepath.EvalSymlinks(view); err == nil {
			view = filepath.ToSlash(real)
		}
	}
	m, ok := longest(l.mounts, view, func(m LayerMount) string { return m.View })
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnmapped, view)
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(view, path.Clean(m.View)), "/")
	if rest == "" {
		return m.Host, nil
	}
	return filepath.Join(m.Host, filepath.FromSlash(rest)), nil
}

// Map names host in this layer: the View of the longest mount Host covering
// it, plus the rest of host. A view is POSIX whatever the host OS.
func (l Layer) Map(host string) (string, error) {
	if l.shared {
		return host, nil
	}
	host = filepath.Clean(host)
	m, ok := longest(l.mounts, host, func(m LayerMount) string { return filepath.Clean(m.Host) })
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnmapped, host)
	}
	rest := filepath.ToSlash(strings.TrimPrefix(host, filepath.Clean(m.Host)))
	return path.Join(m.View, rest), nil
}

// longest is the mount whose root (side of it) covers p most specifically.
func longest(mounts []LayerMount, p string, side func(LayerMount) string) (LayerMount, bool) {
	best, bestLen := -1, -1
	for i, m := range mounts {
		if root := side(m); present.Under(p, root) && len(root) > bestLen {
			best, bestLen = i, len(root)
		}
	}
	if best < 0 {
		return LayerMount{}, false
	}
	return mounts[best], true
}

// childLayer is the layer a mount plan gives a child: each mount's Host is the
// primary's reversal of the controller path it names, its View the target the
// plan chose. Derived from the plan, so it cannot drift from what the daemon
// is asked to bind. A mount the primary cannot reverse is refused here.
func childLayer(primary Layer, mounts []mount) (Layer, error) {
	l := Layer{mounts: make([]LayerMount, 0, len(mounts))}
	for _, m := range mounts {
		host, err := primary.Reverse(m.Host)
		if err != nil {
			return Layer{}, report.Errorf(daemonSourceRemedy, "%w", err)
		}
		l.mounts = append(l.mounts, LayerMount{Host: host, View: m.Container, ReadOnly: m.ReadOnly})
	}
	return l, nil
}

// daemonSourceRemedy is the fix for a path the daemon has no name for.
const daemonSourceRemedy = "put it on a bind mount or volume of ctxloom's own container (a devcontainer typically needs ~/.ctxloom on a volume), or run ctxloom on the daemon's host"

// Crossing is one launch's composition: the controller's layer and the
// child's. Every child-facing string the controller derives goes through
// ToChild, and every absolute path a child reports comes back through
// FromChild, so nothing assumes the two name a file alike.
type Crossing struct{ Primary, Child Layer }

// ToChild names a controller path in the child, and in host path space on
// the way.
func (c Crossing) ToChild(ctl string) (child, host string, err error) {
	host, err = c.Primary.Reverse(ctl)
	if err != nil {
		return "", "", err
	}
	child, err = c.Child.Map(host)
	if err != nil {
		return "", "", err
	}
	return child, host, nil
}

// FromChild names a child path in the controller.
func (c Crossing) FromChild(child string) (string, error) {
	host, err := c.Child.Reverse(child)
	if err != nil {
		return "", err
	}
	return c.Primary.Map(host)
}

// crossingOver is rt's Crossing into a child whose mounts are mounts: the
// placement of the paths they cover, so a path nested in one is named in the
// child through it rather than placed on its own.
func crossingOver(rt Runtime, mounts ...mount) (Crossing, error) {
	primary := rt.primary()
	child, err := childLayer(primary, mounts)
	if err != nil {
		return Crossing{}, err
	}
	return Crossing{Primary: primary, Child: child}, nil
}

// childPath is ToChild over crossingOver(rt, mounts...): ctl as the child
// sees it.
func childPath(rt Runtime, ctl string, mounts ...mount) (string, error) {
	c, err := crossingOver(rt, mounts...)
	if err != nil {
		return "", err
	}
	child, _, err := c.ToChild(ctl)
	return child, err
}

// anchor binds host where rt's placement policy puts it: the mount a
// host-anchored root (the project, a git dir) gets, which the paths nested in
// it are then named through (childPath).
func anchor(rt Runtime, host string, readOnly bool) (m mount, err error) {
	view, err := rt.placement().toContainer(host)
	if err != nil {
		return m, err
	}
	return bind(host, view, readOnly), nil
}

// bind binds host at a target the caller already decided: a
// container-anchored path (under the instance home, /probe), or a path the
// child names through a root's mount (childPath). Every mount this package
// plans is built here or by anchor, so no mount site picks its own target;
// mount.Host stays the path as THIS process sees it, and childLayer reverses
// it at render.
func bind(host, target string, readOnly bool) mount {
	return mount{Host: host, Container: target, ReadOnly: readOnly}
}
