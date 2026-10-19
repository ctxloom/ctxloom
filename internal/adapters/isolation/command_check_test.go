package isolation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubCommandProbe replaces probeExec with a script of the two calls
// MissingCommands makes: the image inspect and the probe container run.
func stubCommandProbe(t *testing.T, imageID string, missing []string) (runs *[][]string) {
	t.Helper()
	commandResults.Clear()
	var got [][]string
	orig := probeExec
	probeExec = func(_ context.Context, _ string, args []string) (string, error) {
		switch args[0] {
		case "image":
			if imageID == "" {
				return "", errors.New("no such image")
			}
			return imageID + "\n", nil
		case "run":
			got = append(got, args)
			return strings.Join(missing, "\n") + "\n", nil
		}
		t.Fatalf("unexpected runtime call %v", args)
		return "", nil
	}
	t.Cleanup(func() { probeExec = orig; commandResults.Clear() })
	return &got
}

func commandContainer() Container {
	return NewContainerFor(fakeRuntime{name: "docker", binary: "docker", available: true}, "claude-code").WithImage("agent:test")
}

func TestMissingCommands_ParsesMissingInOrder(t *testing.T) {
	runs := stubCommandProbe(t, "sha256:aaa", []string{"serena", "uvx"})
	got, err := commandContainer().MissingCommands(context.Background(), []string{"git", "uvx", "serena"})
	require.NoError(t, err)
	assert.Equal(t, []string{"uvx", "serena"}, got)
	require.Len(t, *runs, 1)
	argv := (*runs)[0]
	assert.Equal(t, []string{"run", "--rm", "--network", "none", "--entrypoint", "sh", "agent:test", "-c"}, argv[:8])
}

func TestMissingCommands_NothingMissing(t *testing.T) {
	stubCommandProbe(t, "sha256:aaa", nil)
	got, err := commandContainer().MissingCommands(context.Background(), []string{"git"})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMissingCommands_ImageNotBuilt(t *testing.T) {
	runs := stubCommandProbe(t, "", nil)
	_, err := commandContainer().MissingCommands(context.Background(), []string{"serena"})
	assert.ErrorIs(t, err, ErrImageNotBuilt)
	assert.Empty(t, *runs, "no probe container starts against an image that is not there")
}

func TestMissingCommands_CachesByImageIDAndSortedSet(t *testing.T) {
	runs := stubCommandProbe(t, "sha256:aaa", []string{"serena"})
	c := commandContainer()
	_, err := c.MissingCommands(context.Background(), []string{"serena", "git"})
	require.NoError(t, err)
	got, err := c.MissingCommands(context.Background(), []string{"git", "serena"})
	require.NoError(t, err)
	assert.Equal(t, []string{"serena"}, got)
	assert.Len(t, *runs, 1, "the same image ID and command set probes once, whatever the order")

	// A rebuilt image under the same tag is a new ID and probes again.
	stubID := probeExec
	probeExec = func(ctx context.Context, bin string, args []string) (string, error) {
		if args[0] == "image" {
			return "sha256:bbb\n", nil
		}
		return stubID(ctx, bin, args)
	}
	_, err = c.MissingCommands(context.Background(), []string{"git", "serena"})
	require.NoError(t, err)
	assert.Len(t, *runs, 2)
}

func TestMissingScript_QuotesHostileNames(t *testing.T) {
	script := missingScript([]string{"a'; touch /pwned; '"})
	assert.Contains(t, script, `'a'\''; touch /pwned; '\'''`)
	assert.NotContains(t, script, "command -v -- a'")
}
