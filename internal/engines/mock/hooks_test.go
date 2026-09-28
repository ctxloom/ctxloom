package mock_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
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
	return deliverHookedAt(t, func(u *wire.UnifiedHooks, h wire.Hook) { u.PreTool = []wire.Hook{h} }, hook)
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

// TestMock_AContextFileItCreatesIsOwnerOnly: a managed context file the
// mock creates is 0600 (the engine's own reading needs no wider mode);
// one that already stood keeps its mode.
func TestMock_AContextFileItCreatesIsOwnerOnly(t *testing.T) {
	eng := mock.New()
	project := t.TempDir()
	start := present.ProjectOnHost(project)
	fs := afero.NewOsFs()
	d, err := eng.Root().Context.DeliverContext(start, present.RootProjectRoot, engine.ContextInputs{Text: []byte("ctx")}, fs)
	require.NoError(t, err)
	info, err := os.Stat(d.Presented.HostPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// deliverHookedAt is deliverHooked for any unified event: set places the hook
// on the event under test.
func deliverHookedAt(t *testing.T, set func(u *wire.UnifiedHooks, h wire.Hook), hook wire.Hook) (engine.Engine, engine.Instance, engine.Exec) {
	t.Helper()
	eng := mock.New()
	home := t.TempDir()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"))
	set(&pkg.Hooks.Unified, hook)
	roots := present.Paths{SessionHome: present.Root{Host: home, Engine: home}}
	plan, err := delivery.Route(pkg.EngineItems(eng.Root().Name), eng.Root(), delivery.Preference{}, roots)
	require.NoError(t, err)
	fs := afero.NewOsFs()
	target := delivery.Target{Root: present.New(present.OnHost(roots)), Ownership: deliverytest.NewOwnership(fs), Writer: delivery.SessionWriter("h")}
	d, err := fsstatic.New(fs).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg}, eng.Root(), target)
	require.NoError(t, err)
	require.Contains(t, d.Wrote, present.Hooks, "the hook item was delivered statically")
	inst, err := eng.Instance(engine.Session{Mode: engine.Structured, WorkDir: home})
	require.NoError(t, err)
	ex, err := inst.Exec(d.Presented)
	require.NoError(t, err)
	return eng, inst, ex
}

// TestMock_ATurnStartHookFires_OncePerTurn_WithoutAToolCall: turn_start is
// the prompt's own event — it fires once for every turn, tool call or not,
// and its payload decodes to turn_start through the engine's codec.
func TestMock_ATurnStartHookFires_OncePerTurn_WithoutAToolCall(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fired")
	eng, inst, ex := deliverHookedAt(t, func(u *wire.UnifiedHooks, h wire.Hook) { u.TurnStart = []wire.Hook{h} },
		wire.Hook{Type: "command", Command: "cat >> " + marker + "; echo >> " + marker})

	for _, prompt := range []string{"just answer", "and again"} {
		_, err := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: prompt}, nil)
		require.NoError(t, err)
	}

	raw, err := os.ReadFile(marker)
	require.NoError(t, err, "the hook ran: its command appended the payload to the marker")
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Len(t, lines, 2, "one firing per turn, no more, no fewer:\n%s", raw)
	for _, line := range lines {
		ev, err := eng.Hooks().Decode("turn_start", []byte(line))
		require.NoError(t, err)
		require.Equal(t, "turn_start", ev.Event)
	}
}

// TestMock_FiresEveryUnifiedEvent pins the mock's fire set and its hook-file
// reader against wire.UnifiedHooks: the mock is the conformance backend, so
// an event it cannot fire is an event no hermetic test can prove delivered.
func TestMock_FiresEveryUnifiedEvent(t *testing.T) {
	eng := mock.New()
	exports, err := eng.Exports(engine.Items{})
	require.NoError(t, err)
	typ := reflect.TypeOf(wire.UnifiedHooks{})
	for i := 0; i < typ.NumField(); i++ {
		event := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		assert.Contains(t, exports.HookEvent, event, "the mock declares no %s arm — nothing hermetic can prove that event fires", event)
	}
}
