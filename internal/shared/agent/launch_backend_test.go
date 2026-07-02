package agent

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordLifecycle captures the contextHash MergeManaged receives so Setup tests
// can assert whether the SessionStart context-injection hook was suppressed (a
// "" hash ⇒ no hook appended).
type recordLifecycle struct {
	merged      bool
	contextHash string
}

func (r *recordLifecycle) MergeManaged(_ *ManagedConfig, _ string, contextHash string) {
	r.merged = true
	r.contextHash = contextHash
}
func (r *recordLifecycle) Flush(string) error { return nil }

type noopSkills struct{}

func (noopSkills) RegisterFromContent(string, []CommandExport) error { return nil }

// newSetupBackend wires a LaunchBackend with a capturing lifecycle and a real
// (temp-dir-backed) context provider for Setup tests. history is unused by Setup.
func newSetupBackend() (*LaunchBackend, *recordLifecycle) {
	rec := &recordLifecycle{}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(rec, noopSkills{}, NewBaseContextProvider(), nil)
	return b, rec
}

// TestSetup_NativeContextDelivery_SuppressesHookAndFramesFile pins the injection
// kill: a native-delivery backend materializes the framed context file and hands
// MergeManaged an empty hash, so no SessionStart context-injection hook is
// appended — claude loads the framed file via --append-system-prompt-file.
func TestSetup_NativeContextDelivery_SuppressesHookAndFramesFile(t *testing.T) {
	work := t.TempDir()
	b, rec := newSetupBackend()
	b.EnableNativeContextDelivery()

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   work,
		Fragments: []*Fragment{{Content: "project rules"}},
		Managed:   &ManagedConfig{},
	}))

	require.True(t, rec.merged, "MergeManaged must run")
	assert.Equal(t, "", rec.contextHash,
		"native delivery must withhold the hash so no SessionStart context-injection hook is appended")

	framed := b.NativeContextFilePath()
	require.NotEmpty(t, framed, "native delivery must materialize a framed context file")
	data, err := os.ReadFile(framed)
	require.NoError(t, err)
	assert.Contains(t, string(data), "project rules")
	assert.Contains(t, string(data), ProjectContextHeader,
		"the framed file carries the ctxloom framing, not raw fragments")
}

// TestSetup_HookDelivery_KeepsContextHash proves a non-native backend is
// unchanged: it keeps the context hash (so the SessionStart hook still delivers
// context) and materializes no framed file.
func TestSetup_HookDelivery_KeepsContextHash(t *testing.T) {
	work := t.TempDir()
	b, rec := newSetupBackend() // native delivery NOT enabled

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   work,
		Fragments: []*Fragment{{Content: "project rules"}},
		Managed:   &ManagedConfig{},
	}))

	assert.NotEmpty(t, rec.contextHash,
		"a hook-delivery backend keeps the hash so its SessionStart hook injects context")
	assert.Empty(t, b.NativeContextFilePath(), "no framed file when delivery is via the hook")
}
