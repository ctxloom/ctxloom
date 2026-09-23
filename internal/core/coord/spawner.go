package coord

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// SpawnPlan is a delegated child's launch as the coordinator holds it: the
// SELECTION made at the verb — the binding's name, the engine and label it
// declares, its composed MCP set, its resume mode — and, once StartEngine
// has run under the child's launch context, the RESOLVED Launch. Everything
// the journal and the roster read before the launch is here; everything the
// runner is handed comes off the Launch.
type SpawnPlan struct {
	AgentName string
	// Backend and Label are the binding's DECLARED engine, read at the verb
	// so the roster and the reach-back endpoint know the runtime before the
	// launch resolves; the Launch is authoritative once it exists.
	Backend  string
	Label    string
	Profiles []string
	Runtime  launch.RuntimeAxis
	// Workspace and DirtyTreeHandler are the caller's per-invocation
	// orchestration traits (agent_run's own fields), stamped by AgentRun
	// before the launch resolves; a binding carries no opinion on either.
	Workspace        launch.WorkspaceAxis
	DirtyTreeHandler launch.DirtyTreeHandler
	// Permission is the posture the plan was enqueued with: the binding's
	// declaration (empty when it declared none), or an owner run's floored
	// posture. The launch resolver decides the effective one.
	Permission string
	Degraded   []string
	// MCPServers is the child's composed MCP server set, resolved once at
	// the verb so a later config edit cannot retroactively change a live
	// run's privileges and so the journal, the launch and StartEngine read
	// one set.
	MCPServers []agent.ChatMCPServer
	// ResumeMode is the per-engine resume-capability gate's outcome:
	// ResumeModeOneShot only when the binding declared driving: oneshot AND
	// the engine has a resume-by-key primitive the turn loop is wired for.
	ResumeMode ResumeMode
	// Launch is the resolved launch, set by StartEngine: the cell, the
	// package, the plan, the endpoint, the permission floored once.
	Launch launch.Launch

	// Snapshot is the generation this spawn was selected from; the launch
	// resolves against the same generation, so one spawn observes one state.
	Snapshot *config.Snapshot
}

// ResumeMode is a resolved agent's per-turn engine-lifecycle mode: whether
// its engine process persists across turns — the default, and what every
// conversational agent gets — or tears down at each turn boundary to be
// resumed by native session key on the next mailbox delivery.
type ResumeMode int

const (
	// ResumeModePersistent is the zero value: the engine process stays warm
	// across turns.
	ResumeModePersistent ResumeMode = iota
	// ResumeModeOneShot is the turn-boundary teardown+resume-by-key model,
	// LIVE for the wired backends. Reaching this value requires BOTH a
	// `driving: oneshot` agent declaration and a statically resume-capable
	// backend (resolveResumeMode); Resolve narrows it once more to the
	// backends whose turn loop is wired end to end (oneShotSupportedBackends)
	// and fails loud for any other resume-capable one.
	ResumeModeOneShot
)

// SpawnStart is what StartEngine resolves the child's launch from beyond
// the plan: the child's minted identity and, on a resume, the native key
// the harp's session entry holds.
type SpawnStart struct {
	Identity sessions.Identity
	// ResumeKey is the native session key of the harp being resumed
	// (Spawner.NativeSession); empty on a fresh spawn. A resume reuses the harp's endpoint.
	ResumeKey string
	// Resumed says this is a resume even when no native key survived, so
	// the launch re-resolves the same harp.
	Resumed bool
	// Prompt is the first turn as the coordinator composed it: the caller's
	// prompt on a fresh spawn; the rendered history ahead of it on a resume
	// that has no native key to continue. The runner leads with the
	// package's context ahead of it, so the launch carries the prompt and
	// never the context.
	Prompt string
	// Rebind asks the resolver for a NEW MCP endpoint instead of the one the
	// session record holds: the coordinator's answer to the runner's one
	// refusal, delivery.ErrEndpointUnavailable (another process took the
	// port between two incarnations of the session).
	Rebind bool
}

// Resolved is a resolved launch: the Launch the coordinator journals, reads
// and hands the runner in StartRun. Its wire form is the adapter's, projected
// once by the codec beside the stream it is issued on.
type Resolved struct {
	Launch launch.Launch
}

// Spawner is the coordinator's launch seam: adapters/spawn selects,
// resolves and starts real runners through the launch trunk; tests fake
// children without config or engines. Composed at cmd/*.
type Spawner interface {
	// Resolve SELECTS the binding by name against one fresh generation:
	// the binding must exist and its engine be admitted to delegation; the
	// composed MCP set and the resume mode are decided here. The launch
	// itself resolves in StartEngine, under the child's launch context.
	Resolve(ctx context.Context, agentName string) (*SpawnPlan, error)
	// AssignSession mints the child's harp (its address and continuation
	// token) in the host-side session accounting, recording the engine the
	// selection named (the launch's resolver confirms it), and does NOTHING
	// ELSE.
	//
	// It is on the pre-registration critical path: AgentRun cannot journal
	// run.enqueued, and therefore cannot show the caller that the child
	// exists, until it has an address for it. Anything slow that a
	// starting session also wants — the engine-version probe above all —
	// belongs in RecordEngineVersion, which runs AFTER the run is
	// registered. See task affected-yearly: a probe sitting here left a
	// spawn invisible for minutes, and callers reasonably double-spawned.
	AssignSession(projectDir, backend string) (string, error)
	// RecordEngineVersion probes the child engine's installed CLI and
	// records its version against harp, for the reader selection that
	// happens when this session's transcript is read back.
	//
	// Called from a tracked goroutine AFTER the run is registered, and
	// therefore explicitly NOT on the path that decides whether agent_run
	// returns. It reports nothing: every failure here is a diagnostic that
	// warns at its own level and costs the session nothing (see
	// operations.RecordSessionEngineVersion), exactly like MarkSessionEnded
	// below.
	RecordEngineVersion(ctx context.Context, harp, backend string)
	// BindNativeSession binds the engine's native session key onto harp's
	// session entry (sessions.Entry, the key's one record) with the store's
	// own rule: an unbound entry takes it, a bound one keeps its key. It
	// reports nothing, like MarkSessionEnded.
	BindNativeSession(harp, key string)
	// NativeSession is the native session key harp's session entry holds —
	// what a resume continues — "" while unbound or unreadable.
	NativeSession(harp string) string
	// ResolveLaunch RESOLVES the child's launch (the one resolver: the cell
	// prepared, the permission floored, the endpoint minted or reused —
	// re-minted when start.Rebind). On success plan.Launch is set. It starts
	// nothing: a rebind re-resolves for a runner that is already up.
	ResolveLaunch(ctx context.Context, plan *SpawnPlan, start SpawnStart) (Resolved, error)
	// Start starts the runner for a resolved launch with the reach-back
	// (the coordinator's endpoint and the run's credential) on the runner's
	// env, WITHOUT attaching. Engine control then arrives over the runner's
	// own RunnerChannel (StartRun). The ctx scopes preparation and attach
	// only — spawn.StartRunner's contract.
	Start(ctx context.Context, l launch.Launch, reach sessions.Endpoint) (*EngineSpawn, error)
	// ResumeHistory renders the recorded history of a RESUMED harp for the
	// first turn's lead, "" when none is loadable.
	ResumeHistory(ctx context.Context, harp string) string
	// MarkSessionEnded ends the harp's session: it stamps it ended in session
	// accounting and releases its liveness lock (in production, via
	// operations.EndSession), which is what lets the reaper take the child's
	// session home — credential copy and all — once it ages out.
	MarkSessionEnded(harp string)
}

// EngineSpawn is a Start result: the spawned-but-not-chatting runner
// process.
type EngineSpawn struct {
	// Kill tears the engine process and its cell down (idempotent).
	Kill func()
	// StderrTail reads the runner's bounded stderr tail without reaping —
	// the only death reason available when the whole runner dies without
	// emitting a FAILED RunCompleted. Nil-safe.
	StderrTail func() string
	// Wait blocks until the runner PROCESS exits, reporting why. Nil when
	// the spawner captures no process (test doubles).
	Wait func() error
}

// RunnerHandle is a started runner process as the coordinator holds it. Name
// is the container name for a container runner (the durable teardown
// handle), "" on the host. Kill is idempotent and is the ONE teardown door;
// Wait reaps the process and reports why it exited; StderrTail reads its
// bounded stderr without reaping (nil-safe).
type RunnerHandle struct {
	Name       string
	Kill       func()
	Wait       func() error
	StderrTail func() string
}
