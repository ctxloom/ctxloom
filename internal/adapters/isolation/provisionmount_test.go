package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sharedMaterial(host, destRel string) Material {
	return Material{Host: host, DestRel: destRel, Sharing: SharingShared}
}

// The container implementation emits DESCRIPTORS and touches nothing: the
// mounts happen when the container starts. The target leaf is the DECLARED
// destination name, never one re-derived from the host path — an engine that
// renames material on placement would otherwise be mounted at a path it never
// looks at.
func TestContainerMountProvision_EmitsDescriptorsAtTheDeclaredDestination(t *testing.T) {
	p := &containerMountProvisioner{containerHome: "/root"}
	res, err := p.Provision("/ignored/host/instance", []Material{
		sharedMaterial("/h/.claude/.credentials.json", ".claude/renamed-creds.json"),
	})
	require.NoError(t, err)

	assert.Equal(t, DeliveryMounted, res.Delivery)
	assert.Equal(t, containerMountMechanism, res.Mechanism)
	require.Len(t, res.ContainerMounts, 1)
	assert.Equal(t, "/h/.claude/.credentials.json", res.ContainerMounts[0].Host)
	assert.Equal(t, "/root/.claude/renamed-creds.json", res.ContainerMounts[0].Container,
		"the leaf must be the DECLARED destination name, not path.Base of the host path")
	assert.Empty(t, res.NamespaceBinds, "a container mount is never something this process performs itself")
	assert.NoError(t, res.Close(), "a mount leaves nothing running, so there is nothing to stop")
}

// A credential mount must be WRITABLE: refreshing is a write, and a read-only
// credential is one that cannot renew — the failure this whole design exists
// to remove.
func TestContainerMountProvision_CarriesReadOnlyThroughRatherThanAssumingIt(t *testing.T) {
	p := &containerMountProvisioner{containerHome: "/root"}
	res, err := p.Provision("", []Material{
		sharedMaterial("/h/creds.json", ".claude/creds.json"),
		{Host: "/h/settings.json", DestRel: ".claude/settings.json", Sharing: SharingShared, ReadOnly: true},
	})
	require.NoError(t, err)
	require.Len(t, res.ContainerMounts, 2)
	assert.False(t, res.ContainerMounts[0].ReadOnly, "a credential that cannot be written cannot refresh")
	assert.True(t, res.ContainerMounts[1].ReadOnly)
}

// The namespace implementation PERFORMS the bind itself, so it must first
// stand up the target: a FILE bind mounts over an existing inode and does not
// create one. A missing target would be an ENOENT inside the shim.
func TestNamespaceMountProvision_StandsUpTheMountTargets(t *testing.T) {
	home := t.TempDir()
	hostFile := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(hostFile, []byte("host-token"), 0o600))

	p := &namespaceMountProvisioner{}
	res, err := p.Provision(home, []Material{sharedMaterial(hostFile, ".claude/.credentials.json")})
	require.NoError(t, err)

	assert.Equal(t, DeliveryMounted, res.Delivery)
	assert.Equal(t, namespaceMountMechanism, res.Mechanism)
	require.Len(t, res.NamespaceBinds, 1)
	target := filepath.Join(home, ".claude", ".credentials.json")
	assert.Equal(t, hostFile, res.NamespaceBinds[0].Source)
	assert.Equal(t, target, res.NamespaceBinds[0].Target)
	assert.Empty(t, res.ContainerMounts, "an imperative bind is not a descriptor for somebody else to perform")

	info, err := os.Stat(target)
	require.NoError(t, err, "the mount target must exist before the shim tries to mount over it")
	assert.Zero(t, info.Size(),
		"the target is about to be covered by the host's inode; real bytes there would be material outliving a failed mount")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a placeholder about to hold credential bytes must not be group- or world-readable")
}

// The symlink defense must survive anywhere credential material is placed. A
// repo-tracked link pointing at the user's REAL credential would otherwise
// turn standing up a mount target into an arbitrary-file overwrite.
func TestNamespaceMountProvision_RefusesASymlinkedTarget(t *testing.T) {
	home := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "the-real-credential")
	require.NoError(t, os.WriteFile(elsewhere, []byte("do not touch"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(home, ".claude", ".credentials.json")))

	hostFile := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(hostFile, []byte("host-token"), 0o600))

	p := &namespaceMountProvisioner{}
	_, err := p.Provision(home, []Material{sharedMaterial(hostFile, ".claude/.credentials.json")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")

	survived, err := os.ReadFile(elsewhere)
	require.NoError(t, err)
	assert.Equal(t, "do not touch", string(survived),
		"the write reached through the symlink, which is the arbitrary-file overwrite this defense exists to stop")
}

// Both mount implementations must refuse material whose sharing they do not
// deliver, rather than delivering it approximately.
func TestMountProvision_RefusesMaterialItWouldHaveToSubstitute(t *testing.T) {
	home := t.TempDir()
	hostFile := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(hostFile, []byte("x"), 0o600))

	for name, p := range map[string]Provisioner{
		containerMountMechanism: &containerMountProvisioner{containerHome: "/root"},
		namespaceMountMechanism: &namespaceMountProvisioner{},
	} {
		t.Run(name+"/private", func(t *testing.T) {
			_, err := p.Provision(home, []Material{
				{Host: hostFile, DestRel: "creds.json", Sharing: SharingPrivate},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "does not deliver")
		})
		t.Run(name+"/unset", func(t *testing.T) {
			_, err := p.Provision(home, []Material{{Host: hostFile, DestRel: "creds.json"}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "declares no sharing")
		})
		t.Run(name+"/can", func(t *testing.T) {
			assert.True(t, p.Can(SharingShared))
			assert.False(t, p.Can(SharingPrivate), "a bind mount is the host's own inode; it cannot isolate")
			assert.False(t, p.Can(SharingUnset))
		})
	}
}

// A run that is not containerised has no container home, and the candidate
// must say so rather than emitting descriptors nobody will perform.
func TestNewContainerMountProvisioner_RejectsANonContainerisedRun(t *testing.T) {
	_, err := newContainerMountProvisioner(t.Context(), &provisionConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not containerised")
}
