package operations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// OneShot is a resolved internal one-shot session: ONE minted harp and ONE
// resolved launch, driven a turn at a time. A distill, a triage batch and
// the setup probe are real sessions — a session home, an endpoint, the
// managed surfaces, hooks on — whose turns are frames on the same launch
// with a different prompt. End releases the cell and ends the session.
type OneShot struct {
	Launch    launch.Launch
	store     sessions.Store
	verbosity int
	// Factory overrides the transport (test seam): a non-nil factory drives
	// the turn on the client it builds instead of the cell's own transport.
	Factory pb.ClientFactory
	ended   bool
}

// StartOneShot mints and resolves the one-shot's session. src is the
// caller's Source — Internal with a label for the engine, or an agent
// binding — and is forced Structured, the only mode a turn is driven in.
func StartOneShot(ctx context.Context, deps launch.Deps, seed sessions.Seed, src launch.Source, verbosity int) (*OneShot, error) {
	seed.OneShot = true
	src.Mode = launch.StructuredMode()
	l, err := StartRun(ctx, deps, seed, src)
	if err != nil {
		return nil, err
	}
	return &OneShot{Launch: l, store: deps.Sessions, verbosity: verbosity}, nil
}

// Turn drives one turn: the launch encoded with this turn's prompt, the
// engine run once over the cell's transport, its answer captured and the
// turn recorded on the session's transcript. Exit 0 with no output is a
// failed turn, never an empty answer.
func (o *OneShot) Turn(ctx context.Context, prompt string) (string, error) {
	out, _, err := o.TurnWithModel(ctx, prompt)
	return out, err
}

// TurnWithModel is Turn reporting the model the engine answered with, as the
// engine names it (name, and version when it reports one), for a record that
// attributes the answer.
func (o *OneShot) TurnWithModel(ctx context.Context, prompt string) (answer, model string, err error) {
	if o.ended {
		return "", "", errors.New("one-shot: the session has ended")
	}
	l := o.Launch
	l.Prompt = prompt
	req := coordgrpc.EncodeLaunch(l, o.verbosity)

	factory := o.Factory
	if factory == nil {
		cell, ok := TransportOf(l.Cell)
		if !ok {
			return "", "", errors.New("one-shot: the cell carries no transport handle")
		}
		// A one-shot has no coordinator reach-back by design (its answer is
		// bridged at the boundary), so no per-spawn runner env.
		factory = isolation.FactoryForWorkspace(cell.Policy, cell.Workspace, nil)
	}
	client, err := factory(string(l.Engine), l.Label.Label, o.verbosity)
	if err != nil {
		return "", "", fmt.Errorf("start plugin: %w", err)
	}
	defer client.Kill()

	var stdout, stderr bytes.Buffer
	result, err := client.RunWithModelInfo(ctx, req, nil, &stdout, &stderr, nil)
	if err != nil {
		return "", "", fmt.Errorf("agent run: %w", err)
	}
	if result.ExitCode != 0 {
		return "", "", fmt.Errorf("LLM exited with code %d: %s", result.ExitCode, strings.TrimSpace(stderr.String()))
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		msg := fmt.Sprintf("agent produced no output: %s exited 0 with an empty stdout", l.Engine)
		if e := strings.TrimSpace(stderr.String()); e != "" {
			msg += ": " + e
		}
		return "", "", errors.New(msg)
	}
	model = string(l.Engine)
	if info := result.ModelInfo; info != nil {
		if info.ModelName != "" {
			model = info.ModelName
		}
		if info.ModelVersion != "" {
			model = fmt.Sprintf("%s:%s", model, info.ModelVersion)
		}
	}
	// The turn returns prose on stdout with no event stream, so the
	// structured capture never fires for it; record it on the session's own
	// transcript. Best-effort: a capture failure must never fail the turn.
	if terr := transcript.RecordOneshot(l.Identity.Harp, string(l.Engine), prompt, stdout.String()); terr != nil {
		clidiag.Warn("ctxloom", "one-shot transcript capture: %v", terr)
	}
	return out, model, nil
}

// End releases the cell and ends the session. Idempotent.
func (o *OneShot) End() {
	if o.ended {
		return
	}
	o.ended = true
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
	start func(ctx context.Context) (*OneShot, error)
	mu    sync.Mutex
	os    *OneShot
}

// NewLazyOneShot defers StartInternalOneShot to the first turn.
func NewLazyOneShot(cfg *config.Config, label, model, workDir, projectID string, verbosity int) *LazyOneShot {
	return &LazyOneShot{start: func(ctx context.Context) (*OneShot, error) {
		return StartInternalOneShot(ctx, cfg, label, model, workDir, projectID, verbosity)
	}}
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
// the label naming its engine (model overridden when the caller says so).
func InternalSource(label, model, workDir string) launch.Source {
	return launch.Source{Internal: true, Label: label, Model: model, WorkDir: workDir}
}

// StartInternalOneShot mints and resolves an internal one-shot over the
// generation cfg belongs to: the compactor's distiller, the trigger
// evaluator's triage, the setup probe. projectID is the identity the
// session serves (empty when the caller resolved none).
func StartInternalOneShot(ctx context.Context, cfg *config.Config, label, model, workDir, projectID string, verbosity int) (*OneShot, error) {
	// A Config built outside the Owner carries no Trust: refuse here, at the
	// entry point, rather than let the assembler withhold every executable
	// with the "no authorizer" defect reason.
	if _, err := cfg.RequireTrust(); err != nil {
		return nil, fmt.Errorf("internal one-shot: %w", err)
	}
	deps, err := LaunchDepsFor(&config.Snapshot{Config: cfg})
	if err != nil {
		return nil, err
	}
	return StartOneShot(ctx, deps, sessions.Seed{ProjectDir: workDir, ProjectID: projectID}, InternalSource(label, model, workDir), verbosity)
}

// runtimeCarrier / containerPersister are the narrow capabilities the container
// policy implements (Runtime / ContainerPersistDir) and None/Worktree do not —
// probed here rather than widening isolation.Policy, mirroring
// mcpCommandOverrider. They feed the docker-exec interactive launcher (Phase
// 2a-A): the runtime renders `exec -it`, and the container persist dir is where
// the in-container turn reads the RunStart handoff.
type runtimeCarrier interface{ Runtime() isolation.Runtime }
type containerPersister interface{ ContainerPersistDir(harp string) string }

// RuntimeForPolicy reports a container policy's launch runtime (docker/podman),
// or nil for none/worktree — the seam the docker-exec vpio.Launcher renders
// `exec -it` through.
func RuntimeForPolicy(p isolation.Policy) isolation.Runtime {
	if rc, ok := p.(runtimeCarrier); ok {
		return rc.Runtime()
	}
	return nil
}

// ContainerPersistDirForPolicy reports the IN-CONTAINER path the host's session
// persist dir is bind-mounted to for a container policy (where the docker-exec
// turn reads the RunStart handoff), or "" for none/worktree.
func ContainerPersistDirForPolicy(p isolation.Policy, harp string) string {
	if cp, ok := p.(containerPersister); ok {
		return cp.ContainerPersistDir(harp)
	}
	return ""
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
// MODE HANDLING: this gate no longer tests strictness.Degraded() itself. It
// filters to its own class and passes the result through strictness.Actionable,
// the ONE place the mode is consulted — so under --degraded a DEGRADABLE
// isolation finding still lets the fan run, while a NON-DEGRADABLE one (a
// requested container boundary that could not be provided, an image that can
// start as root) fails the member in both modes.
//
// The `if Degraded() { return nil }` this replaces was the amplifier for every
// bypass the degradation audit found: it switched the whole gate off, so
// converting the raise sites without converting this would have changed
// nothing at all. A class-filtered gate must filter and then defer to
// Actionable — never short-circuit on the mode.
func isolationGateErr(found []strictness.Finding) error {
	var iso []strictness.Finding
	for _, f := range found {
		if f.Class == strictness.ClassIsolation {
			iso = append(iso, f)
		}
	}
	if iso = strictness.Actionable(iso); len(iso) == 0 {
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
func ResolveBackend(cfg *config.Config, label string) (backend, model string) {
	backend, model = cfg.ResolveLLM(label)
	_, configured := cfg.GetLLMEntry(label)
	if !configured && backends.Exists(label) {
		return label, ""
	}
	if !configured && label != "" {
		strictness.Fail(strictness.ClassConfig,
			fmt.Sprintf("add an `llm:` entry for %q in .ctxloom/config.yaml, or name one of the configured labels (%s) or a known engine (%s)",
				label, knownLLMLabels(cfg), strings.Join(backends.List(), ", ")),
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
