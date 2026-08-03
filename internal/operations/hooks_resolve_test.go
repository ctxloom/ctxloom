package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// cfgWithHooks builds a config carrying exactly these config-level hooks. It is
// the smallest input that exercises the resolver without a bundle tree on disk.
func cfgWithHooks(t *testing.T, h wire.UnifiedHooks) *config.Config {
	t.Helper()
	return config.NewFixture(config.Fixture{Hooks: wire.HooksConfig{Unified: h}})
}

// TestResolveHooks_ReportsFinalOrderPerEvent is the whole point of the surface:
// the order a user actually gets is emergent across sources and, before this,
// invisible. Twelve hooks in one event, because ten is where a width or lexical
// bug shows.
func TestResolveHooks_ReportsFinalOrderPerEvent(t *testing.T) {
	var hooks []wire.Hook
	var want []string
	for i := 0; i < 12; i++ {
		cmd := "cmd-" + string(rune('a'+i))
		want = append(want, cmd)
		hooks = append(hooks, wire.Hook{Type: "command", Command: cmd})
	}
	res, err := ResolveHooks(context.Background(), ResolveHooksRequest{
		ConfigLoader: func() (*config.Config, error) {
			return cfgWithHooks(t, wire.UnifiedHooks{PreTool: hooks}), nil
		},
		WorkDir: t.TempDir(),
	})
	require.NoError(t, err)

	ev := eventNamed(t, res, "pre_tool")
	var got []string
	for i, h := range ev.Hooks {
		assert.Equal(t, i+1, h.Position, "positions must be 1-based and contiguous")
		got = append(got, h.Command)
	}
	assert.Equal(t, want, got, "the reported order must be the FINAL resolved order")
}

// Every one of the six events must be reported, including the empty ones. An
// event omitted because it is empty reads as "ctxloom did not look", and the
// question the user asked — what runs on session_end? — goes unanswered.
func TestResolveHooks_ReportsAllSixEventsEvenWhenEmpty(t *testing.T) {
	res, err := ResolveHooks(context.Background(), ResolveHooksRequest{
		ConfigLoader: func() (*config.Config, error) {
			return cfgWithHooks(t, wire.UnifiedHooks{PreTool: []wire.Hook{{Type: "command", Command: "x"}}}), nil
		},
		WorkDir: t.TempDir(),
	})
	require.NoError(t, err)

	var events []string
	for _, e := range res.Events {
		events = append(events, e.Event)
	}
	assert.Equal(t, []string{
		"pre_tool", "post_tool", "session_start", "session_end", "pre_shell", "post_file_edit",
	}, events)
}

// Provenance is half the answer. "Which hooks, in what order" without "from
// where" leaves a user who dislikes the order with nowhere to go: they cannot
// tell which bundle to talk to, or whether the hook is even theirs to change.
func TestResolveHooks_NamesEachHooksSource(t *testing.T) {
	res, err := ResolveHooks(context.Background(), ResolveHooksRequest{
		ConfigLoader: func() (*config.Config, error) {
			return cfgWithHooks(t, wire.UnifiedHooks{PreTool: []wire.Hook{
				{Type: "command", Command: "local-one"},
				{Type: "command", Command: "from-bundle", SCM: "bundle:acme/tools"},
			}}), nil
		},
		WorkDir: t.TempDir(),
	})
	require.NoError(t, err)

	ev := eventNamed(t, res, "pre_tool")
	require.Len(t, ev.Hooks, 2)
	assert.Equal(t, SourceKindLocal, ev.Hooks[0].SourceKind,
		"a hook with no bundle marker is local config or an inline profile, and must say so rather than claim a bundle")
	assert.Empty(t, ev.Hooks[0].Source)
	assert.Equal(t, SourceKindBundle, ev.Hooks[1].SourceKind)
	assert.Equal(t, "acme/tools", ev.Hooks[1].Source,
		"the bundle ref must be reported bare, not as the internal 'bundle:' marker")
}

// Filtering to one event must not change the ANSWER for that event, only what is
// printed. A filter that re-resolved from a narrower input could report an order
// the user will never actually get.
func TestResolveHooks_EventFilterNarrowsWithoutChangingTheAnswer(t *testing.T) {
	load := func() (*config.Config, error) {
		return cfgWithHooks(t, wire.UnifiedHooks{
			PreTool:      []wire.Hook{{Type: "command", Command: "p1"}, {Type: "command", Command: "p2"}},
			SessionStart: []wire.Hook{{Type: "command", Command: "s1"}},
		}), nil
	}
	dir := t.TempDir()
	all, err := ResolveHooks(context.Background(), ResolveHooksRequest{ConfigLoader: load, WorkDir: dir})
	require.NoError(t, err)
	one, err := ResolveHooks(context.Background(), ResolveHooksRequest{ConfigLoader: load, WorkDir: dir, Event: "pre_tool"})
	require.NoError(t, err)

	require.Len(t, one.Events, 1)
	assert.Equal(t, eventNamed(t, all, "pre_tool").Hooks, one.Events[0].Hooks)
}

// An unknown event name is a typo, and a typo must not resolve to "nothing runs
// here" — that is a confident, wrong answer. ctxloom's characteristic bug is exit
// 0 with an empty result, and an inspect command is the last place it belongs.
func TestResolveHooks_UnknownEventIsRefusedNotAnsweredEmpty(t *testing.T) {
	_, err := ResolveHooks(context.Background(), ResolveHooksRequest{
		ConfigLoader: func() (*config.Config, error) { return cfgWithHooks(t, wire.UnifiedHooks{}), nil },
		WorkDir:      t.TempDir(),
		Event:        "pre_toll",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre_toll")
}

// Backend-native ("plugin") hooks bypass the six unified events entirely. They
// are still hooks that run, so a surface reporting only the unified six would
// quietly under-report what is wired — the same class of silent omission the
// order field exists to prevent.
func TestResolveHooks_BackendNativeHooksAreReportedNotSilentlyDropped(t *testing.T) {
	res, err := ResolveHooks(context.Background(), ResolveHooksRequest{
		ConfigLoader: func() (*config.Config, error) {
			return config.NewFixture(config.Fixture{Hooks: wire.HooksConfig{
				Plugins: map[string]wire.BackendHooks{
					"claude-code": {"PreCompact": []wire.Hook{{Type: "command", Command: "native"}}},
				},
			}}), nil
		},
		WorkDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, res.BackendNative, 1)
	assert.Equal(t, "claude-code", res.BackendNative[0].Backend)
	assert.Equal(t, "PreCompact", res.BackendNative[0].Event)
	require.Len(t, res.BackendNative[0].Hooks, 1)
	assert.Equal(t, "native", res.BackendNative[0].Hooks[0].Command)
}

func eventNamed(t *testing.T, res *ResolveHooksResult, event string) ResolvedHookEvent {
	t.Helper()
	for _, e := range res.Events {
		if e.Event == event {
			return e
		}
	}
	t.Fatalf("event %q not reported; got %+v", event, res.Events)
	return ResolvedHookEvent{}
}
