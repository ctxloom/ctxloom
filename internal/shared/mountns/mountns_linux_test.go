//go:build linux

package mountns_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/mountns"
)

// TestMain is what makes this package testable at all: Supported re-execs
// os.Executable(), which under `go test` is the TEST BINARY. Without this the
// shim process would re-run the whole test suite instead of performing the
// mounts, so the probe would report the host incapable on every host.
func TestMain(m *testing.M) {
	mountns.RunChildIfRequested()
	os.Exit(m.Run())
}

// The probe must answer YES on a host that permits the namespace, and it must
// answer for the right reasons — the assertions below are what Supported
// checks internally, restated here against files this test owns so a probe
// that started passing vacuously is caught.
func TestSupported_OnAPermissiveHost(t *testing.T) {
	if err := mountns.Supported(context.Background(), t.TempDir()); err != nil {
		t.Skipf("this host does not permit unprivileged user namespaces, which is a legitimate answer: %v", err)
	}
}

// A bind established by the shim must be visible to the process the shim
// EXECS, not merely to the shim: the engine is the grandchild, so a mount
// that did not survive the exec would be a mount nothing that matters sees.
func TestCommand_TheExecdProcessSeesTheBind(t *testing.T) {
	requireNamespaces(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(source, []byte("host-material"), 0o600))
	require.NoError(t, os.WriteFile(target, []byte("instance-placeholder"), 0o600))

	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat on this host")
	}
	cmd, err := mountns.Command(context.Background(), []mountns.Bind{{Source: source, Target: target}}, []string{cat, target}, os.Environ())
	require.NoError(t, err)
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "host-material", string(out),
		"the exec'd process read the placeholder, so the bind did not survive the shim's exec")

	// And nothing leaked: the host still sees its own file at that path.
	onHost, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "instance-placeholder", string(onHost),
		"the mount escaped its namespace and is visible on the host")
}

// A write through the mount must reach the HOST file. This is the property a
// live credential refresh depends on: rename(2) over a bind mount returns
// EBUSY, the writer falls back to writing in place, and that write has to land
// on the host inode or the instance has diverged silently.
func TestCommand_AnInPlaceWriteThroughTheBindReachesTheHostFile(t *testing.T) {
	requireNamespaces(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(source, []byte("before"), 0o600))
	require.NoError(t, os.WriteFile(target, []byte("placeholder"), 0o600))

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host")
	}
	// printf > file truncates and writes in place; it does not rename.
	cmd, err := mountns.Command(context.Background(),
		[]mountns.Bind{{Source: source, Target: target}},
		[]string{sh, "-c", "printf after > " + target}, os.Environ())
	require.NoError(t, err)
	require.NoError(t, cmd.Run())

	landed, err := os.ReadFile(source)
	require.NoError(t, err)
	assert.Equal(t, "after", string(landed),
		"the in-place write did not propagate to the host file through the bind")
}

// A read-only bind must actually refuse writes, or ReadOnly is decoration.
func TestCommand_AReadOnlyBindRefusesAWrite(t *testing.T) {
	requireNamespaces(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(source, []byte("before"), 0o600))
	require.NoError(t, os.WriteFile(target, []byte("placeholder"), 0o600))

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host")
	}
	cmd, err := mountns.Command(context.Background(),
		[]mountns.Bind{{Source: source, Target: target, ReadOnly: true}},
		[]string{sh, "-c", "printf after > " + target}, os.Environ())
	require.NoError(t, err)
	assert.Error(t, cmd.Run(), "a write to a read-only bind succeeded")

	landed, err := os.ReadFile(source)
	require.NoError(t, err)
	assert.Equal(t, "before", string(landed), "a read-only bind let a write through to the host")
}

// Command must refuse an empty argv rather than build a shim that has nothing
// to become: a shim whose exec never happens is a process that falls through
// into whatever the binary does by default.
func TestCommand_RefusesAnEmptyArgv(t *testing.T) {
	_, err := mountns.Command(context.Background(), nil, nil, os.Environ())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "argv")
}

func requireNamespaces(t *testing.T) {
	t.Helper()
	if err := mountns.Supported(context.Background(), t.TempDir()); err != nil {
		t.Skipf("this host does not permit unprivileged user namespaces: %v", err)
	}
}
