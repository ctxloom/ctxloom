package mock_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/delivery/deliverytest"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// deliverHooked delivers a package carrying one pre_tool hook (its command
// copies the hook's stdin to marker) into a session home on the real
// filesystem, and binds an instance to that session.
func deliverHooked(t *testing.T, hook wire.Hook) (engine.Engine, engine.Instance, engine.Exec) {
	t.Helper()
	eng := mock.New()
	home := t.TempDir()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"))
	pkg.Hooks.Unified.PreTool = []wire.Hook{hook}
	roots := present.Paths{Scratch: present.Root{Host: home, Engine: home}}
	plan, err := delivery.Route(pkg.EngineItems(eng.Root().Name), eng.Root(), delivery.Preference{}, roots)
	require.NoError(t, err)
	fs := afero.NewOsFs()
	target := delivery.Target{Root: present.New(present.OnHost(roots)), Ownership: deliverytest.NewOwnership(fs), Writer: delivery.SessionWriter("h")}
	d, err := fsstatic.New(fs).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg}, eng.Root().Surfaces(), target)
	require.NoError(t, err)
	require.Contains(t, d.Wrote, present.Hooks, "the hook item was delivered statically")
	inst, err := eng.Instance(engine.Session{Mode: engine.Structured, WorkDir: home})
	require.NoError(t, err)
	ex, err := inst.Exec(d.Presented)
	require.NoError(t, err)
	return eng, inst, ex
}

// TestMock_ADeliveredPreToolHookFires_WhenTheTurnRunsATool is the hook
// ruling's probe over the mock: a hook delivered as a static item FIRES
// when the engine's turn runs a tool, and its effect is read back as a
// deterministic marker — the payload the engine wrote to the hook's stdin,
// which the engine's own codec decodes to the unified event.
func TestMock_ADeliveredPreToolHookFires_WhenTheTurnRunsATool(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fired")
	eng, inst, ex := deliverHooked(t, wire.Hook{Type: "command", Command: "cat > " + marker})

	res, err := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.ToolCall("Bash") + " then answer"}, nil)
	require.NoError(t, err)

	payload, err := os.ReadFile(marker)
	require.NoError(t, err, "the hook ran: its command copied the payload to the marker")
	ev, err := eng.Hooks().Decode("pre_tool", payload)
	require.NoError(t, err)
	require.Equal(t, "pre_tool", ev.Event)
	require.Equal(t, res.NativeKey, ev.NativeSession, "the payload names the engine's own session")
}

// TestMock_AHookDoesNotFire_WithoutAToolCall: the same delivered hook is
// inert on a turn that runs no tool — firing is the engine reacting to a
// tool call, not a side effect of delivery.
func TestMock_AHookDoesNotFire_WithoutAToolCall(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fired")
	_, inst, ex := deliverHooked(t, wire.Hook{Type: "command", Command: "cat > " + marker})
	_, err := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: "just answer"}, nil)
	require.NoError(t, err)
	require.NoFileExists(t, marker)
}

// TestMock_AHookWithAMatcherFires_OnlyForTheToolItNames: the matcher is
// honoured — the hook fires for the tool it names and for no other.
func TestMock_AHookWithAMatcherFires_OnlyForTheToolItNames(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fired")
	_, inst, ex := deliverHooked(t, wire.Hook{Type: "command", Matcher: "Edit", Command: "cat > " + marker})
	_, err := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.ToolCall("Bash")}, nil)
	require.NoError(t, err)
	require.NoFileExists(t, marker, "Bash does not match Edit")
	_, err = inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.ToolCall("Edit")}, nil)
	require.NoError(t, err)
	require.FileExists(t, marker)
}

// TestMock_HooksCodec_RefusesAPayloadItDidNotWrite: the codec decodes the
// mock's own payload shape and nothing else.
func TestMock_HooksCodec_RefusesAPayloadItDidNotWrite(t *testing.T) {
	_, err := mock.New().Hooks().Decode("pre_tool", []byte("not json"))
	require.Error(t, err)
}

