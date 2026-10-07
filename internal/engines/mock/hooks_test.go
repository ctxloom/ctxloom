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
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
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

	res, err := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.ToolCall(string(wire.ToolShell)) + " then answer"}, nil)
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
	_, inst, ex := deliverHooked(t, wire.Hook{Type: "command", Matcher: "file_edit", Command: "cat > " + marker})
	_, err := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.ToolCall(string(wire.ToolShell))}, nil)
	require.NoError(t, err)
	require.NoFileExists(t, marker, "shell does not match file_edit")
	_, err = inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.ToolCall(string(wire.ToolFileEdit))}, nil)
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
// mock's delivery creates is 0600 (the engine's own reading needs no wider
// mode); one that already stood keeps its mode.
func TestMock_AContextFileItCreatesIsOwnerOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		seed bool
		want os.FileMode
	}{"created": {false, 0o600}, "already standing": {true, 0o644}} {
		t.Run(name, func(t *testing.T) {
			eng := mock.New()
			project := t.TempDir()
			path := filepath.Join(project, mock.ContextFileName)
			if tc.seed {
				require.NoError(t, os.WriteFile(path, []byte("# mine\n"), 0o644))
				require.NoError(t, os.Chmod(path, 0o644))
			}
			fs := afero.NewOsFs()
			rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
			require.NoError(t, err)
			pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "ctx"))
			start := present.ProjectOnHost(project)
			plan, err := delivery.Route(pkg.EngineItems(eng.Root().Name), eng.Root(), delivery.Preference{}, start.Paths())
			require.NoError(t, err)
			_, err = fsstatic.New(safefs.NewMem(fs)).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg}, eng.Root(),
				delivery.Target{Root: start, Ownership: rec, Writer: delivery.ProjectWriter})
			require.NoError(t, err)
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.Equal(t, tc.want, info.Mode().Perm())
		})
	}
}

// deliverHookedAt is deliverHooked for any unified event: set places the hook
// on the event under test.
func deliverHookedAt(t *testing.T, set func(u *wire.UnifiedHooks, h wire.Hook), hook wire.Hook) (engine.Engine, engine.Instance, engine.Exec) {
	t.Helper()
	return deliverHookedOn(t, mock.New(), set, hook)
}

// deliverHookedOn is deliverHookedAt on a given kind.
func deliverHookedOn(t *testing.T, eng engine.Engine, set func(u *wire.UnifiedHooks, h wire.Hook), hook wire.Hook) (engine.Engine, engine.Instance, engine.Exec) {
	t.Helper()
	home := t.TempDir()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"))
	set(&pkg.Hooks.Unified, hook)
	roots := present.Paths{SessionHome: present.Root{Host: home, Engine: home}}
	plan, err := delivery.Route(pkg.EngineItems(eng.Root().Name), eng.Root(), delivery.Preference{}, roots)
	require.NoError(t, err)
	fs := afero.NewOsFs()
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	target := delivery.Target{Root: present.New(present.OnHost(roots)), Ownership: rec, Writer: delivery.SessionWriter("h")}
	d, err := fsstatic.New(safefs.NewMem(fs)).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg}, eng.Root(), target)
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

// TestMock_ToolClassesNarrowTheToolEvents: the mock's tool vocabulary is the
// neutral one, so a shell-class call fires pre_shell and a file-edit-class
// call fires post_file_edit — and neither fires the other's.
//
// MUTATION -- narrow on any other tool name in eventsOfTurn -- turns this red.
func TestMock_ToolClassesNarrowTheToolEvents(t *testing.T) {
	dir := t.TempDir()
	shell, edit := filepath.Join(dir, "shell"), filepath.Join(dir, "edit")
	_, inst, ex := deliverHookedAt(t, func(u *wire.UnifiedHooks, _ wire.Hook) {
		u.PreShell = []wire.Hook{{Type: "command", Command: "cat >> " + shell}}
		u.PostFileEdit = []wire.Hook{{Type: "command", Command: "cat >> " + edit}}
	}, wire.Hook{})
	_, err := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.ToolCall(string(wire.ToolShell))}, nil)
	require.NoError(t, err)
	require.FileExists(t, shell)
	require.NoFileExists(t, edit, "a shell call is no file edit")
	require.NoError(t, os.Remove(shell))
	_, err = inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.ToolCall(string(wire.ToolFileEdit))}, nil)
	require.NoError(t, err)
	require.FileExists(t, edit)
	require.NoFileExists(t, shell, "a file edit is no shell call")
}

// TestMock_AHookNarrowedToAToolClass_FiresOnlyForThatTool: a hook carrying a
// tool class (wire.Hook.Tool) is bound to the mock's tool of that class when
// it is delivered, so it fires for that tool and no other.
//
// MUTATION -- make the mock's toolMatcher admit every tool -- turns this red.
func TestMock_AHookNarrowedToAToolClass_FiresOnlyForThatTool(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fired")
	_, inst, ex := deliverHookedAt(t, func(u *wire.UnifiedHooks, h wire.Hook) { u.PostTool = []wire.Hook{h} },
		wire.Hook{Type: "command", Tool: wire.ToolSkill, Command: "cat > " + marker})
	_, err := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.ToolCall(string(wire.ToolShell))}, nil)
	require.NoError(t, err)
	require.NoFileExists(t, marker, "a shell call is not the skill tool")
	_, err = inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.ToolCall(string(wire.ToolSkill))}, nil)
	require.NoError(t, err)
	require.FileExists(t, marker)
}

// TestMock_ADeliveredCallbackNamesTheDouble: a ctxloom callback delivered to
// a mock double names that double as the engine firing it — the double's own
// name, never the base mock's — so the verb decodes through its codec.
func TestMock_ADeliveredCallbackNamesTheDouble(t *testing.T) {
	_, _, ex := deliverHookedAt(t, func(u *wire.UnifiedHooks, h wire.Hook) { u.TurnEnd = []wire.Hook{h} }, agent.NewNextStepHook())
	file := ""
	for i, a := range ex.Args {
		if a == mock.HooksFlag && i+1 < len(ex.Args) {
			file = ex.Args[i+1]
		}
	}
	hooks, err := mock.DeliveredHooksFile(file)
	require.NoError(t, err)
	require.Len(t, hooks.TurnEnd, 1)
	require.Equal(t, []string{"hook", "next-step", agent.HookEngineFlag, string(mock.Name)}, hooks.TurnEnd[0].Args)
}

// TestMock_HooksCodec_RoundTripsItsOwnWire: the mock's codec decodes the
// fields its own payload carries, encodes a response in its own shape, names
// a skill only for its skill tool, and bounds no context.
func TestMock_HooksCodec_RoundTripsItsOwnWire(t *testing.T) {
	codec := mock.New().Hooks()
	ev, err := codec.Decode("turn_start", []byte(`{"event":"turn_start","session":"k","tool":"shell","prompt":"p"}`))
	require.NoError(t, err)
	require.Equal(t, engine.HookEvent{Event: "turn_start", NativeSession: "k", Tool: "shell", Prompt: "p"}, ev)

	reply, err := codec.Encode("turn_start", engine.HookResponse{Context: "c", Block: true, Reason: "r"})
	require.NoError(t, err)
	require.JSONEq(t, `{"context":"c","block":true,"reason":"r"}`, string(reply.Stdout))

	name, ok := codec.InvokedSkill(string(wire.ToolSkill), []byte(`"closeout"`))
	require.True(t, ok)
	require.Equal(t, "closeout", name)
	_, ok = codec.InvokedSkill(string(wire.ToolShell), []byte(`"closeout"`))
	require.False(t, ok)
	require.Zero(t, codec.ContextLimit())
}

// TestMock_TheLossyDoublesHookFileOmitsItsDeclaredLosses: the lossy double
// declares session_start and session_end lost (it neither exports nor fires
// them), so its delivered hook file carries no hook for either — the file
// agrees with the loss report — while every event it does fire is kept.
//
// MUTATION -- stop clearing the lost events in hooksFile.DeliverHooks --
// turns this red.
func TestMock_TheLossyDoublesHookFileOmitsItsDeclaredLosses(t *testing.T) {
	eng := mock.NewNamed(mock.NameLossy, mock.WithoutHookEvents("session_start", "session_end"))
	lost := eng.Root().HookLosses
	require.NotEmpty(t, lost, "precondition: the lossy double declares hook losses")
	h := wire.Hook{Type: "command", Command: "true"}
	_, _, ex := deliverHookedOn(t, eng, func(u *wire.UnifiedHooks, _ wire.Hook) {
		u.SessionStart, u.SessionEnd, u.TurnStart = []wire.Hook{h}, []wire.Hook{h}, []wire.Hook{h}
	}, h)
	file := ""
	for i, a := range ex.Args {
		if a == mock.HooksFlag && i+1 < len(ex.Args) {
			file = ex.Args[i+1]
		}
	}
	require.NotEmpty(t, file, "the hook file is announced on --hooks")
	got, err := mock.DeliveredHooksFile(file)
	require.NoError(t, err)
	for event := range lost {
		assert.Empty(t, got.Event(event), "%s is declared lost and must not be in the file", event)
	}
	assert.Len(t, got.TurnStart, 1, "an event the double fires is kept")
}
