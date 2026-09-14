package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/mountns"
)

// The two MOUNT implementations. They are in one file because they are each
// other's contrast, and separate TYPES because they share a kernel mechanism
// and nothing else — see candidatesFor's doc for why collapsing them would be
// a mode flag with a branch per difference.
//
// Both deliver SHARED BY IDENTITY: one inode, seen by the host and by the
// instance, so a token the engine refreshes is the host's token. Neither can
// deliver SharingPrivate, and both say so through Can rather than by
// approximating it.
//
// Why identity is safe for a credential that refreshes itself: claude writes a
// credential by RENAME, and a rename onto a bind-mounted path returns EBUSY.
// Its writer routes a rename failure by errno through a set that contains
// EBUSY and falls back to writing the target IN PLACE, which reaches through
// the mount to the host file — measured, both in the shipped binary and end to
// end. That is a fact about one engine at one version, read from minified
// internal names with no stability contract, and a future release could change
// it silently.

const (
	containerMountMechanism = "container-mount"
	namespaceMountMechanism = "namespace-mount"
)

// NamespaceBind is one imperative bind this process performs itself. Aliased
// rather than redefined so a Result's binds are exactly what the shim takes,
// with no translation layer to drift.
type NamespaceBind = mountns.Bind

// containerMountProvisioner emits bind DESCRIPTORS for a container runtime to
// perform. It never touches the filesystem: the mounts happen when the
// container starts, and a bad one fails there, reported by docker or podman,
// not here.
type containerMountProvisioner struct {
	// containerHome is the engine's home INSIDE the container — the root every
	// emitted mount target hangs off.
	containerHome string
}

// newContainerMountProvisioner probes by asking whether this run is
// containerised at all. There is no capability question beyond that: container
// mounting is available whenever the runtime is, and the runtime's own
// availability was already decided by SelectRuntime before a container home
// existed to pass here.
func newContainerMountProvisioner(_ context.Context, cfg *provisionConfig) (Provisioner, error) {
	if cfg.containerHome == "" {
		return nil, errors.New("this run is not containerised, so there is no container home to mount material into and no runtime to perform the mounts")
	}
	return &containerMountProvisioner{containerHome: cfg.containerHome}, nil
}

// Mechanism names this implementation.
func (p *containerMountProvisioner) Mechanism() string { return containerMountMechanism }

// Delivery reports shared-by-identity.
func (p *containerMountProvisioner) Delivery() Delivery { return DeliveryMounted }

// Can reports that a mount delivers shared material and nothing else. It does
// NOT answer true for SharingPrivate: a bind mount is the host's own inode, so
// claiming to isolate it would be the silent substitution this design forbids.
func (p *containerMountProvisioner) Can(s Sharing) bool { return s == SharingShared }

// Provision emits one descriptor per material.
//
// instanceHome is deliberately unused: a container's engine home lives in the
// container's own filesystem, not in a directory on this host, so the target
// root is the container home this provisioner was constructed with. The
// argument stays in the signature because the interface is shared with
// implementations that do place files on this host.
func (p *containerMountProvisioner) Provision(_ string, materials []Material) (Result, error) {
	res := Result{Delivery: DeliveryMounted, Mechanism: containerMountMechanism}
	for _, m := range materials {
		if err := validateMaterial(m, SharingShared); err != nil {
			return Result{}, err
		}
		// path.Join, not filepath.Join, and DestRel verbatim: the target is a
		// path inside the container (always slash-separated, whatever this
		// host uses), and the leaf is the DECLARED destination name rather
		// than one re-derived from the host path — an engine that renames
		// material on placement would otherwise be mounted at a path it never
		// looks at.
		res.ContainerMounts = append(res.ContainerMounts, Mount{
			Host:      m.Host,
			Container: path.Join(p.containerHome, m.DestRel),
			ReadOnly:  m.ReadOnly,
		})
	}
	return res, nil
}

// namespaceMountProvisioner performs the binds ITSELF, with mount(2), inside a
// user+mount namespace it creates through a re-exec shim. Failure is an errno
// in our own shim before the engine is exec'd, not a container-start error
// from someone else's runtime — which is the whole reason it is not the
// container implementation with a flag.
type namespaceMountProvisioner struct{}

// newNamespaceMountProvisioner PROBES AT CONSTRUCTION, which is the point:
// unprivileged user namespaces are a kernel- and policy-dependent privilege,
// never a property of GOOS, so an unusable mechanism must fail while it is
// being built rather than mid-run with a live engine attached.
func newNamespaceMountProvisioner(ctx context.Context, cfg *provisionConfig) (Provisioner, error) {
	if err := cfg.namespaceProbe(ctx, cfg.scratch); err != nil {
		return nil, err
	}
	return &namespaceMountProvisioner{}, nil
}

// defaultNamespaceProbe is the real capability check. Select replaces it only
// for a negative control — see provisionConfig.namespaceProbe.
func defaultNamespaceProbe(ctx context.Context, scratch string) error {
	return mountns.Supported(ctx, scratch)
}

// Mechanism names this implementation.
func (p *namespaceMountProvisioner) Mechanism() string { return namespaceMountMechanism }

// Delivery reports shared-by-identity.
func (p *namespaceMountProvisioner) Delivery() Delivery { return DeliveryMounted }

// Can reports that a mount delivers shared material and nothing else, for the
// same reason as the container implementation.
func (p *namespaceMountProvisioner) Can(s Sharing) bool { return s == SharingShared }

// Provision stands up the mount TARGETS inside instanceHome and returns the
// binds the shim must perform.
//
// The targets have to exist first: a FILE bind mount mounts over an existing
// inode, it does not create one, so a missing target is an ENOENT inside the
// shim rather than an empty file the engine quietly reads. They are created
// through iox.WriteFileInPlace, which refuses a symlinked destination AT THE
// OPEN SYSCALL — carrying forward the defense that sits beside the copy path
// today, where a repo-tracked link pointing at the user's real credential
// would otherwise turn placement into an arbitrary-file overwrite.
func (p *namespaceMountProvisioner) Provision(instanceHome string, materials []Material) (Result, error) {
	if instanceHome == "" {
		return Result{}, errors.New("namespace-mount provisioning: no instance home to mount material into")
	}
	res := Result{Delivery: DeliveryMounted, Mechanism: namespaceMountMechanism}
	for _, m := range materials {
		if err := validateMaterial(m, SharingShared); err != nil {
			return Result{}, err
		}
		target := filepath.Join(instanceHome, filepath.FromSlash(m.DestRel))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return Result{}, fmt.Errorf("namespace-mount provisioning: prepare %s: %w", filepath.Dir(target), err)
		}
		// AllowEmpty: an empty file is exactly what a mount target should be.
		// It is about to be covered by the host's inode, and leaving real
		// bytes there would be material that outlives a failed mount.
		if err := iox.WriteFileInPlace(target, iox.TruncateInPlace, nil, 0o600, iox.AllowEmpty()); err != nil {
			return Result{}, fmt.Errorf("namespace-mount provisioning: stand up the mount target %s: %w", target, err)
		}
		res.NamespaceBinds = append(res.NamespaceBinds, NamespaceBind{
			Source:   m.Host,
			Target:   target,
			ReadOnly: m.ReadOnly,
		})
	}
	return res, nil
}

// validateMaterial refuses a material this provisioner would have to guess
// about. want is the sharing this implementation delivers; anything else is a
// caller asking for something it will not get, and it is refused here rather
// than delivered approximately.
func validateMaterial(m Material, want Sharing) error {
	if m.Host == "" {
		return errors.New("material has no host path")
	}
	if m.DestRel == "" {
		return fmt.Errorf("material %s has no destination inside the instance home", m.Host)
	}
	if m.Sharing == SharingUnset {
		return fmt.Errorf("material %s declares no sharing; SharingUnset is nobody's decision and is refused rather than guessed at", m.Host)
	}
	if m.Sharing != want {
		return fmt.Errorf("material %s asks for %s sharing, which this provisioner does not deliver (it delivers %s); a provisioner that quietly delivered the other one is the substitution this design exists to forbid", m.Host, m.Sharing, want)
	}
	return nil
}
