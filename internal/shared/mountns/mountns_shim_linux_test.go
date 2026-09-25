package mountns

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestShim_RefusesAMalformedSpecBeforeMounting pins the shim's decode step.
// Every case fails before the first mount(2), which is what makes calling
// shim in-process safe here.
func TestShim_RefusesAMalformedSpecBeforeMounting(t *testing.T) {
	cases := []struct {
		name, binds, argv, wantErr string
	}{
		{"binds not JSON", "{", `["/bin/true"]`, "decode binds: unexpected end of JSON input"},
		{"argv not JSON", "[]", "{", "decode argv: unexpected end of JSON input"},
		{"argv empty", "null", "[]", "no argv to exec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envBinds, tc.binds)
			t.Setenv(envArgv, tc.argv)
			require.EqualError(t, shim(), tc.wantErr)
		})
	}
}

// TestShim_ABindThatCannotBeMadeEndsTheChild: a bind whose source is missing
// fails inside the namespace; the shim reports which bind and exits non-zero
// without ever exec'ing the target.
func TestShim_ABindThatCannotBeMadeEndsTheChild(t *testing.T) {
	if err := Supported(context.Background(), t.TempDir()); err != nil {
		t.Skipf("this host does not permit unprivileged user namespaces: %v", err)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "missing")
	target := filepath.Join(dir, "target")
	marker := filepath.Join(dir, "ran")
	require.NoError(t, os.WriteFile(target, []byte("placeholder"), 0o600))

	cmd, err := Command(context.Background(), []Bind{{Source: source, Target: target}},
		[]string{sh, "-c", "touch " + marker}, os.Environ())
	require.NoError(t, err)
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "mountns shim: bind "+source+" onto "+target+": no such file or directory")
	require.NoFileExists(t, marker, "the shim exec'd its target after a failed bind")
}
