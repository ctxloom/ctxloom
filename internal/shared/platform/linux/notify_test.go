//go:build unix

package linux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotify_RunsNotifySendWithTheTextAfterItsOptions: the resolved
// notify-send gets the app name, then "--", then title and body as given —
// a body that starts with "-" stays text.
func TestNotify_RunsNotifySendWithTheTextAfterItsOptions(t *testing.T) {
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	script := filepath.Join(dir, "notify-send")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+argv+"\"\n"), 0o755))
	var asked string
	resolve := func(name string) (string, error) { asked = name; return script, nil }

	require.NoError(t, OS{}.Notify(context.Background(), resolve, "ctxloom: approval waiting", "-n 1 waiting"))

	assert.Equal(t, "notify-send", asked)
	got, err := os.ReadFile(argv)
	require.NoError(t, err)
	assert.Equal(t, []string{"--app-name=ctxloom", "--", "ctxloom: approval waiting", "-n 1 waiting"}, strings.Split(strings.TrimSuffix(string(got), "\n"), "\n"))
}

// TestNotify_NoNotifySendIsSilent: a host without notify-send shows nothing
// and reports nothing.
func TestNotify_NoNotifySendIsSilent(t *testing.T) {
	resolve := func(string) (string, error) { return "", exec.ErrNotFound }
	assert.NoError(t, OS{}.Notify(context.Background(), resolve, "t", "b"))
}

// TestNotify_AFailingNotifierIsReported: notify-send that fails (no session
// bus) is the caller's to ignore, not hidden here.
func TestNotify_AFailingNotifierIsReported(t *testing.T) {
	script := filepath.Join(t.TempDir(), "notify-send")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0o755))
	err := OS{}.Notify(context.Background(), func(string) (string, error) { return script, nil }, "t", "b")
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), "%v", err)
	assert.Equal(t, 3, exit.ExitCode())
}
