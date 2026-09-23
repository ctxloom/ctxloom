package operations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// OneShotSession is a resolved internal one-shot session: ONE minted harp, ONE
// resolved launch, ONE owner-owned run on the coordinator that hosts it,
// driven a turn at a time. A distill, a triage batch and the setup probe are
// real sessions — a session home, an endpoint, the managed surfaces, hooks
// on — whose turns are frames to the same runner with a different prompt.
// The runner is the unit: the run's runner starts through the cell's
// transport (RunnerStarter) and parks on an empty briefing; every turn is a
// Coordinator.Turn frame to that same runner, answered at its boundary. End
// ends the run, releases the cell and ends the session.
type OneShotSession struct {
	Launch launch.Launch
	host   RunHost
	runID  string
	// kill ends the runner the starter recorded; nil until it started.
	kill  func()
	store sessions.Store
	ended bool
}

// RunHost is the coordinator surface an internal one-shot's run rides on.
// *coord.Coordinator is the production value.
type RunHost interface {
	Owner() coord.Identity
	StartOwnedRun(ctx context.Context, owner coord.Identity, spec coord.OwnerRun, start coord.OwnedRunStarter, prompt string) (*coord.RunOutcome, error)
	Turn(ctx context.Context, runID string, t engine.Turn) (engine.TurnResult, error)
}

// RunHosts yields the coordinator a one-shot's run rides on, for the harp
// the session was minted as: the session's own coordinator when the process
// hosts one (the host tools of a live session), or one the command hosts for
// the purpose (a distill, the setup probe) — a project has ONE coordinator,
// owned by a session harp, so the harp comes first and the host after.
type RunHosts interface {
	RunHost(ctx context.Context, projectDir, ownerHarp string) (RunHost, error)
}

// RunHostFunc adapts a function to RunHosts.
type RunHostFunc func(ctx context.Context, projectDir, ownerHarp string) (RunHost, error)

// RunHost implements RunHosts.
func (f RunHostFunc) RunHost(ctx context.Context, projectDir, ownerHarp string) (RunHost, error) {
	return f(ctx, projectDir, ownerHarp)
}

// ErrNoRunHost refuses an internal one-shot with no coordinator to run on:
// every engine turn is a runner's, and a runner is started and turned by a
// coordinator; there is no second arm to drive one without.
var ErrNoRunHost = errors.New("internal one-shot: no coordinator hosts this run (the runner is started and turned by one)")

// StartOneShot mints and resolves the one-shot's session, then starts its
// run on the coordinator hosts yields for it: the runner through the cell's
// transport, parked with no briefing until the first Turn. src is the caller's Source — Internal with
// a label for the engine, or an agent binding — and is forced Structured,
// the only mode a turn is driven in.
func StartOneShot(ctx context.Context, deps launch.Deps, hosts RunHosts, seed sessions.Seed, src launch.Source, verbosity int) (*OneShotSession, error) {
	if hosts == nil {
		return nil, ErrNoRunHost
	}
	seed.OneShot = true
	src.Mode = launch.StructuredMode()
	l, err := StartRun(ctx, deps, seed, src)
	if err != nil {
		return nil, err
	}
	o := &OneShotSession{Launch: l, store: deps.Sessions}
	host, err := hosts.RunHost(ctx, seed.ProjectDir, l.Identity.Harp)
	if err != nil {
		o.End()
		return nil, fmt.Errorf("one-shot %s: %w", l.Identity.Harp, err)
	}
	if host == nil {
		o.End()
		return nil, ErrNoRunHost
	}
	o.host = host
	start := o.starter(verbosity)
	// A structured run, not the coordinator's ONE-SHOT kind: that kind gets
	// exactly one turn and must open with it, while this session takes each
	// of its turns as a frame — the launch's identity already says it is a
	// one-shot session (seed.OneShot).
	rebind := func(ctx context.Context, l launch.Launch) (launch.Launch, error) {
		return launch.RebindEndpoint(ctx, deps, l)
	}
	outcome, err := host.StartOwnedRun(ctx, host.Owner(), coord.OwnerRun{Launch: l, Rebind: rebind}, start, "")
	if err != nil {
		o.End()
		return nil, fmt.Errorf("one-shot %s: start run: %w", l.Identity.Harp, err)
	}
	o.runID = outcome.RunID
	return o, nil
}

// starter is the run's OwnedRunStarter: the cell's transport (RunnerStarter),
// recording the runner's handle for End. A cell prepared elsewhere (a test
// double) carries no transport, which is the starter's own refusal — the
// coordinator asks for the runner only when it starts the run.
func (o *OneShotSession) starter(verbosity int) coord.OwnedRunStarter {
	l := o.Launch
	cell, ok := TransportOf(l.Cell)
	if !ok {
		return func(context.Context, map[string]string) (func(), string, error) {
			return nil, "", errors.New("one-shot: the cell carries no transport handle")
		}
	}
	return RunnerStarter(cell, string(l.Engine), l.Label.Label, verbosity, func(h *isolation.RunnerHandle) { o.kill = h.Kill })
}

// Turn drives one turn on the parked runner — a fresh engine process resumed
// by the key the runner learned — and returns its answer, recorded on the
// session's transcript by the runner. An empty answer is a failed turn, never
// an empty answer.
func (o *OneShotSession) Turn(ctx context.Context, prompt string) (string, error) {
	out, _, err := o.TurnWithModel(ctx, prompt)
	return out, err
}

// TurnWithModel is Turn reporting the model the turn ran on as the launch
// names it — the label's model when it configures one, else the engine —
// for a record that attributes the answer.
func (o *OneShotSession) TurnWithModel(ctx context.Context, prompt string) (answer, model string, err error) {
	if o.ended {
		return "", "", errors.New("one-shot: the session has ended")
	}
	l := o.Launch
	res, err := o.host.Turn(ctx, o.runID, engine.Turn{Prompt: prompt})
	if err != nil {
		return "", "", fmt.Errorf("agent run: %w", err)
	}
	out := strings.TrimSpace(res.Answer)
	if out == "" {
		return "", "", fmt.Errorf("agent produced no output: %s answered the turn with nothing", l.Engine)
	}
	model = string(l.Engine)
	if l.Label.Model != "" {
		model = l.Label.Model
	}
	return out, model, nil
}

// End ends the run's runner, releases the cell and ends the session.
// Idempotent.
func (o *OneShotSession) End() {
	if o.ended {
		return
	}
	o.ended = true
	if o.kill != nil {
		o.kill()
	}
	if err := launch.Discard(context.Background(), o.Launch); err != nil {
		clidiag.Warn("ctxloom", "one-shot %s: release cell: %v", o.Launch.Identity.Harp, err)
	}
	if err := EndSessionIn(o.store, o.Launch.Identity.Harp, time.Now()); err != nil {
		clidiag.Warn("ctxloom", "one-shot %s: end session: %v", o.Launch.Identity.Harp, err)
	}
}

// LazyOneShot is an internal one-shot whose session starts on the FIRST
// turn: a caller that never turns (a compaction served from its cache)
// mints nothing. Turn is assignable to memory.Runner; End releases the
// session if one started.
type LazyOneShot struct {
	start func(ctx context.Context) (*OneShotSession, error)
	mu    sync.Mutex
	os    *OneShotSession
}

// Turn drives one turn, starting the session first if none has.
func (l *LazyOneShot) Turn(ctx context.Context, prompt string) (string, error) {
	l.mu.Lock()
	if l.os == nil {
		o, err := l.start(ctx)
		if err != nil {
			l.mu.Unlock()
			return "", err
		}
		l.os = o
	}
	o := l.os
	l.mu.Unlock()
	return o.Turn(ctx, prompt)
}

// End releases the session, if one started. Idempotent.
func (l *LazyOneShot) End() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.os != nil {
		l.os.End()
		l.os = nil
	}
}

// InternalSource is the Source an internal one-shot asks with: no binding,
// the label naming its engine (model overridden when the caller says so),
// at plan. An internal one-shot (distill, triage) only reads and answers,
// and its payload can carry transcript text, so it must never run at a
// posture that could act on what that text says. The posture rides the
// flag rung, so no label or project declaration widens it; on an engine
// that cannot enforce plan the run is refused. A caller that is not
// distilling (the setup probe) overrides Permission.
func InternalSource(label, model, workDir string) launch.Source {
	return launch.Source{Internal: true, Label: label, Model: model, WorkDir: workDir, Permission: engine.PermissionPlan}
}

// ErrOneShotNoWorkDir refuses an internal one-shot with no working
// directory: it is the project the session is minted under and the cell the
// run is hosted for, so a launch without one has nowhere correct to run.
var ErrOneShotNoWorkDir = errors.New("internal one-shot: no working directory")

// OneShotBuilder assembles an internal one-shot over the generation cfg
// belongs to, run on the coordinator hosts yields: the compactor's
// distiller, the trigger evaluator's triage, premise authoring. No field has
// a default: an unset Label resolves the way any launch's does (the
// project's primary), an unset Model is the label's own, and WorkDir is
// required (Start refuses without it).
type OneShotBuilder struct {
	facts                 LaunchFacts
	hosts                 RunHosts
	cfg                   *config.Config
	label, model, workDir string
}

// OneShot starts an internal one-shot under f, on the coordinator hosts
// yields, over cfg's generation.
func OneShot(f LaunchFacts, hosts RunHosts, cfg *config.Config) *OneShotBuilder {
	return &OneShotBuilder{facts: f, hosts: hosts, cfg: cfg}
}

// Label names the LLM label the one-shot runs on.
func (b *OneShotBuilder) Label(label string) *OneShotBuilder { b.label = label; return b }

// Model overrides the label's model for this one-shot.
func (b *OneShotBuilder) Model(model string) *OneShotBuilder { b.model = model; return b }

// WorkDir is the project the session is minted under and runs in.
func (b *OneShotBuilder) WorkDir(dir string) *OneShotBuilder { b.workDir = dir; return b }

// Start mints and resolves the one-shot and starts its run now. It refuses
// facts missing a composed port (Build's sentinels), an empty WorkDir
// (ErrOneShotNoWorkDir) and a Config with no Trust.
func (b *OneShotBuilder) Start(ctx context.Context) (*OneShotSession, error) {
	if _, err := NewLaunchFacts(b.facts.Engines).Claims(b.facts.SessionClaims).Mode(b.facts.Mode).Build(); err != nil {
		return nil, err
	}
	if b.workDir == "" {
		return nil, ErrOneShotNoWorkDir
	}
	// A Config built outside the Owner carries no Trust: refuse here, at the
	// entry point, rather than let the assembler withhold every executable
	// with the "no authorizer" defect reason.
	if _, err := b.cfg.RequireTrust(); err != nil {
		return nil, fmt.Errorf("internal one-shot: %w", err)
	}
	deps, err := LaunchDepsFor(b.facts, &config.Snapshot{Config: b.cfg})
	if err != nil {
		return nil, err
	}
	return StartOneShot(ctx, deps, b.hosts, sessions.Seed{ProjectDir: b.workDir}, InternalSource(b.label, b.model, b.workDir), 0)
}

// Lazy defers Start to the one-shot's first turn: a caller that never turns
// (a compaction served from its cache) mints nothing. Start's refusals
// surface from that turn.
func (b *OneShotBuilder) Lazy() *LazyOneShot {
	built := *b
	return &LazyOneShot{start: built.Start}
}

// runtimeCarrier is the narrow capability the container policy implements
// (Runtime) and None/Worktree do not — probed here rather than widening
// isolation.Policy. It is how the originator awaits a container runner's
// running state (isolation.AwaitContainerRunning).
type runtimeCarrier interface{ Runtime() isolation.Runtime }

// RuntimeForPolicy reports a container policy's launch runtime (docker/podman),
// or nil for none/worktree.
func RuntimeForPolicy(p isolation.Policy) isolation.Runtime {
	if rc, ok := p.(runtimeCarrier); ok {
		return rc.Runtime()
	}
	return nil
}

// isolationGateErr is the fail-loudly member gate over the strictness findings
// collected during one member's isolation.Prepare: a ClassIsolation finding
// means an explicitly-requested isolation guarantee could not be satisfied AS
// REQUESTED and the prepared workspace silently degraded toward a WEAKER
// posture than asked for. Two distinct cases share this class today: (1) a
// requested CONTAINER couldn't start and the run fell back to the bare,
// unsandboxed host; (2) a binding that declared engine_home: session has no
// credentials to seed and no API-key env, so the engine would launch logged
// out. Either way, running the member as-is would silently
// deliver less than what was asked for. That fails THE MEMBER
// (an error Part in the fan; other members continue — partial success is
// still success), never the whole call. The finding's own Message fully
// describes WHICH case fired — this wrapper adds no case-specific wording, so
// it never misdescribes one case using the other's vocabulary.
//
// MODE HANDLING: this gate never tests the mode itself. It
// filters to its own class and passes the result through strictness.Actionable,
// the ONE place the mode is consulted — so under --degraded a DEGRADABLE
// isolation finding still lets the fan run, while a NON-DEGRADABLE one (a
// requested container boundary that could not be provided, an image that can
// start as root) fails the member in both modes.
//
// A `return nil` under degraded here would be the amplifier for every
// bypass the degradation audit found: it switched the whole gate off, so
// converting the raise sites without converting this would have changed
// nothing at all. A class-filtered gate must filter and then defer to
// Actionable — never short-circuit on the mode.
func isolationGateErr(mode strictness.Mode, found []strictness.Finding) error {
	var iso []strictness.Finding
	for _, f := range found {
		if f.Class == strictness.ClassIsolation {
			iso = append(iso, f)
		}
	}
	if iso = mode.Actionable(iso); len(iso) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("isolation: refusing to run this member — an explicitly-requested isolation guarantee could not be satisfied")
	for _, f := range iso {
		fmt.Fprintf(&b, ": %s", f.Message)
	}
	if fix := iso[0].FixIt; fix != "" {
		fmt.Fprintf(&b, " (fix: %s)", fix)
	}
	return errors.New(b.String())
}

// ResolveBackend maps a config label to its backend type and model. A label that
// is not a configured entry but names a registered backend type resolves to that
// backend directly (the ad-hoc `--llm <type>` convenience); otherwise
// cfg.ResolveLLM's lookup/default applies. Shared by `run` and the
// oneshot/agent_run path so backend resolution is identical everywhere.
//
// This is the promotion boundary: on one side a label the user typed, on the
// other a BACKEND NAME the whole launch path keys tables by — including
// internal/adapters/isolation's credential-seed, instance-config and projector
// tables, which resolve engines by exact name. An engine has one spelling, so
// the name leaves here as the registry holds it: the ad-hoc arm admits only a
// registered name, and a configured entry's type is validated on write.
// A LABEL THAT NAMES NOTHING IS A FINDING, raised here and nowhere else. It
// used to resolve to the built-in default backend with no warning in EITHER
// mode — not a degrade at all, since nothing consulted the mode — so a retired
// alias (`claude` once resolved through an alias table) or a plain typo routed
// the run to a different engine than the one named, invisibly.
//
// THIS is the layer that can tell that apart from the legitimate form: a label
// with no `llm:` entry that names a known BACKEND is supported and common
// (`llm: mock` with no config block at all). Only once BOTH halves are false
// does the label name nothing, and only then is refusing correct.
// config.ResolveLLM cannot make that call — it has no backend registry — and an
// earlier cut of this audit raised the finding there and fired it on correct
// configurations.
//
// DEGRADABLE (Fail, not FailAlways) on the audit's own test: does LAUNCHING
// cause the harm? It does not — the harm is the substitution being invisible,
// not the run proceeding. Falling back to a working default under --degraded is
// precisely what the standing promise in cli/version_gate.go protects. An empty
// label is exempt: nothing was named, so there is nothing to refuse.
func ResolveBackend(reg engine.Registry, cfg *config.Config, label string) (backend, model string) {
	backend, model = cfg.ResolveLLM(label)
	_, configured := cfg.GetLLMEntry(label)
	if !configured && EngineExists(reg, label) {
		return label, ""
	}
	if !configured && label != "" {
		strictness.Fail(strictness.ClassConfig,
			fmt.Sprintf("add an `llm:` entry for %q in .ctxloom/config.yaml, or name one of the configured labels (%s) or a known engine (%s)",
				label, knownLLMLabels(cfg), strings.Join(EngineNames(reg), ", ")),
			"llm label %q names neither a configured `llm:` entry nor a known engine; this run would silently use the built-in default backend %q instead of the engine you named",
			label, backend)
	}
	return backend, model
}

// knownLLMLabels renders the configured label set for a fix-it line, sorted so
// the sentence is stable across runs — map order would otherwise reshuffle it
// and make one recurring fault read as several different ones.
func knownLLMLabels(cfg *config.Config) string {
	entries := cfg.GetLMConfig().Configs
	if len(entries) == 0 {
		return "none configured"
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// resolveOneshotLabel picks the config label for a oneshot run: an explicit
// override wins, then the profile's declared llm, then the primary role. Unknown
// labels degrade through cfg.ResolveLLM (→ default backend), so a stale profile
// llm never blocks the run (CLAUDE.md fault tolerance).
func resolveOneshotLabel(cfg *config.Config, override, profileLLM string) string {
	if override != "" {
		return override
	}
	if profileLLM != "" {
		return profileLLM
	}
	return cfg.PrimaryLabel()
}
