package config

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// TestLoadRaw_UnresolvableHomeWarnsInsteadOfDroppingSilently pins that losing
// the entire home config layer is never silent.
//
// os.UserHomeDir fails whenever HOME is unset — a container, a cron job, a
// systemd unit with a scrubbed environment. When it does, the home layer is
// dropped and the merge proceeds project-only: a user's ~/.taskloom/config.yaml
// setting `homing: repo` stops applying and taskloom reads and writes a
// DIFFERENT task store than the one it used yesterday, with nothing said. The
// degrade itself is right (one unreadable layer must not block startup — the
// same policy loadRaw already applies to a malformed --config-set entry), but
// it must be announced, which is what the warning below asserts. The payload
// assertion is deliberate: exit status and a nil error say nothing about which
// layers actually contributed.
func TestLoadRaw_UnresolvableHomeWarnsInsteadOfDroppingSilently(t *testing.T) {
	home := taskstest.Isolate(t)
	writeConfig(t, home, "homing: repo\n")
	project := t.TempDir()

	// The file above still exists on disk; it is only the LOCATION that
	// becomes unresolvable.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	merged, err := loadRaw(project, nil)
	require.NoError(t, err, "an unresolvable home must still degrade to project-only, never block the load")
	assert.NotContains(t, merged, HomingConfigKey,
		"the home layer really is dropped — that silent loss is what the warning has to announce")
	assert.Contains(t, sink.String(), "home config location unresolved",
		"dropping the whole home config layer must not be silent")
}

// TestLoadRaw_ResolvableHomeIsQuiet is the negative case: the warning above
// must fire only on the fault, never on an ordinary invocation. A tool that
// warns on every run trains its users to ignore it, which is the same failure
// mode in a smaller costume (see TestHoming_MissingConfigIsSilent).
func TestLoadRaw_ResolvableHomeIsQuiet(t *testing.T) {
	home := taskstest.Isolate(t)
	writeConfig(t, home, "homing: repo\n")
	project := t.TempDir()

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	merged, err := loadRaw(project, nil)
	require.NoError(t, err)
	assert.Equal(t, "repo", merged[HomingConfigKey], "the home layer must apply when HOME resolves")
	assert.Empty(t, sink.String(), "a resolvable home must produce no diagnostic at all")
}
