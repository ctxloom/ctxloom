package operations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// This file is the trunk every host-side launch enters: StartRun mints the
// identity and resolves the launch through launch.Resolve, with the ports
// Resolve needs composed once by LaunchDeps. The cells adapter here is the
// ONE place the three launch paths used to each prepare a workspace, bind
// the engine home and commit around a delegated child's worktree.

// StartRun mints the session identity for seed and resolves src through the
// one resolver, then records the version of the binary the resolved launch
// spawns against the minted harp (deps.ProbeVersion; recordLaunchedVersion).
// The caller starts the transport it owns from the Launch and
// ends the session (EndSession) when the run is over; a launch that does not
// resolve ends its own session here so no harp is left half-minted. That
// failed launch's Launch still carries the minted Identity, so the caller can
// record why the session ended (sessions.Entry.EndReason); it is the zero
// Launch when nothing was minted.
func StartRun(ctx context.Context, deps launch.Deps, seed sessions.Seed, src launch.Source) (launch.Launch, error) {
	base, err := OutputBase(deps.Snapshot.Config)
	if err != nil {
		return launch.Launch{}, err
	}
	id, err := MintIdentity(deps.Sessions, seed, base)
	if err != nil {
		return launch.Launch{}, err
	}
	src.Identity = id
	if deps.ClaimCheck == nil {
		deps = deps.ForSession(id.Harp)
	}
	l, err := launch.Resolve(ctx, deps, src)
	if err != nil {
		if eerr := EndSessionIn(deps.Sessions, id.Harp, time.Now()); eerr != nil {
			clidiag.Warn("ctxloom", "session %s: end after a refused launch: %v", id.Harp, eerr)
		}
		return launch.Launch{Identity: id}, err
	}
	recordLaunchedVersion(ctx, deps, l)
	return l, nil
}

// recordLaunchedVersion records, against l's harp, the version of the binary
// l spawns: l's engine at its label binary. It runs here, once the launch
// resolves, rather than in MintIdentity, because the mint does not know the
// engine or the binary yet — Resolve decides both.
//
// WHY EVERY TOP-LEVEL RUN NEEDS IT: a session with no recorded version is
// refused by every transcript reader (vendorreader.NoRecordedVersionError),
// the turn-end hooks (next-step, skill-mates) included. A delegated child is
// recorded by the coordinator (coord's recordEngineVersion); this is the same
// record for the originator's own session.
//
// It is synchronous, so the record is in the index before the engine's first
// turn can end. The probe is cached per process and bounded
// (engineversion.DefaultProbeTimeout); a failed one warns and the run goes on.
func recordLaunchedVersion(ctx context.Context, deps launch.Deps, l launch.Launch) {
	if deps.ProbeVersion == nil {
		return
	}
	version, perr := deps.ProbeVersion(ctx, string(l.Engine), l.Label.Binary)
	recordProbedVersion(deps.Sessions, l.Identity.Harp, version, perr)
}

// MintIdentity is THE mint on the host: the harp assigned in the store (its
// directory and sidecar), the liveness lock held by this process, the
// identity returned. The engine is not known here — Resolve decides it and
// records it (Store.BindEngine), and StartRun records its version. The session's output dir is recorded under outputBase (OutputBase); a session that cannot
// say where its outputs go is refused.
func MintIdentity(store sessions.Store, seed sessions.Seed, outputBase string) (sessions.Identity, error) {
	entry, err := store.AssignHarp(seed.ProjectDir, seed.Engine)
	if err != nil {
		return sessions.Identity{}, fmt.Errorf("session naming failed, refusing to run: %w", err)
	}
	if _, err := store.RecordOutputDir(entry.HarpName, outputBase); err != nil {
		return sessions.Identity{}, fmt.Errorf("session %s: recording its output dir, refusing to run: %w", entry.HarpName, err)
	}
	// A failed stamp warns rather than refuses: an unstamped session reads as
	// a human's, the reading a sweep never purges undistilled.
	if oerr := store.StampMint(entry.HarpName, sessions.MintStamp{Origin: seed.Origin()}); oerr != nil {
		clidiag.Warn("ctxloom", "session %s: cannot record its origin, so a sweep will treat it as a human's session: %v", entry.HarpName, oerr)
	}
	// THIS PROCESS OWNS THE SESSION FROM HERE: hold its liveness lock until
	// EndSession. A failed hold leaves NO lock file, so the harp reads
	// Indeterminate — never reclaimed — rather than Dead. The lock guards the
	// session's on-disk data; a session minted into the in-memory store (a
	// `run --dry-run` preview) has none, so it takes no lock and leaves no
	// file behind.
	if _, inMemory := store.(*sessions.MemStore); !inMemory {
		if herr := sessionlock.Hold(entry.HarpName); herr != nil {
			clidiag.Warn("ctxloom", "session %s: cannot hold its liveness lock, so its data will never be reaped as crashed: %v", entry.HarpName, herr)
		}
	}
	// The originator's own run is depth 0 and never at the cap: its leafness
	// is its one-shot-ness alone. A child's is the coordinator's verdict.
	return sessions.Identity{Harp: entry.HarpName, Depth: seed.Depth, OneShot: seed.OneShot, Leaf: seed.OneShot, Project: seed.ProjectID}, nil
}

// LaunchDeps composes the resolver's ports over this process's published
// generation, with this App's cached engine-version probe as ProbeVersion, so
// a session started on them records the version it ran (StartRun).
func (a *App) LaunchDeps(ctx context.Context) (launch.Deps, error) {
	snap, err := a.Snapshot(ctx)
	if err != nil {
		return launch.Deps{}, err
	}
	deps, err := LaunchDepsFor(a.LaunchFacts(), snap)
	if err != nil {
		return launch.Deps{}, err
	}
	deps.ProbeVersion = func(ctx context.Context, engine, binary string) (string, error) {
		return probeEngineVersion(a, ctx, engine, binary)
	}
	return deps, nil
}

// LaunchDepsFor composes the resolver's ports over one generation: the
// composed engines, the assembler and cells adapters, the endpoint minter,
// the session store, the host facts and the per-session claim constructor
// ForSession roots once the harp is minted. A caller holding only the
// generation's Config (the compactor, the trigger evaluator) wraps it in a
// Snapshot; Resolve reads the Config and nothing else off it.
func LaunchDepsFor(f LaunchFacts, snap *config.Snapshot) (launch.Deps, error) {
	reg, mode := f.Engines, f.Mode
	store, err := openSessions()
	if err != nil {
		return launch.Deps{}, err
	}
	host, err := hostFacts()
	if err != nil {
		return launch.Deps{}, err
	}
	return launch.Deps{
		Snapshot:      snap,
		Engines:       reg,
		Assembler:     &assembler{engines: reg},
		Cells:         Cells{cfg: snap.Config, mode: mode, engines: reg},
		Endpoints:     endpointMinter{},
		Sessions:      store,
		Inline:        composite.Inline{Max: composite.DefaultInlineMax},
		SessionClaims: f.SessionClaims,
		InlineMax:     composite.DefaultInlineMax,
		Host:          host,
		Reporter:      mode.Sink(),
	}, nil
}

// Opened is a launch's package as the local launcher reads it: the decoded
// package, the Loadout the static writer consumes (the same value the
// runner builds: launch.Launch.Loadout), and the managed payload whose MCP
// server names the owner run's journal records.
type Opened struct {
	Package composite.Package
	Loadout delivery.Loadout
	Managed *agent.ManagedConfig
}

// OpenLaunch is the in-process consumer of the carrier (launch.Open): the
// same package and the same Loadout the runner builds for a delegated
// launch, so the two deliver one set.
func OpenLaunch(ctx context.Context, deps launch.Deps, l launch.Launch) (Opened, error) {
	pkg, err := launch.Open(ctx, deps, l)
	if err != nil {
		return Opened{}, fmt.Errorf("open the launch's package: %w", err)
	}
	return Opened{Package: pkg, Loadout: l.Loadout(pkg), Managed: agent.ManagedConfigFor(ManagedSurfacesOf(pkg), l.Exports)}, nil
}

// PreviewClaims is the claim check a --dry-run carries with: in memory, so a
// preview of a package above the inline ceiling stows nothing on disk.
func PreviewClaims() composite.Transport { return composite.ClaimCheck{Store: memClaims{}} }

type memClaims map[string][]byte

func (m memClaims) Put(_ context.Context, digest [32]byte, b []byte) (string, error) {
	m[string(digest[:])] = b
	return string(digest[:]), nil
}
func (m memClaims) Get(_ context.Context, loc string) ([]byte, error) { return m[loc], nil }

// hostFacts is the originator's host facts: the real home, the ctxloom home
// and the binary. The home is read through the one reader core/paths keeps
// (the ctxloom home is home/AppDirName by that reader's construction, so
// the real home is its parent); operations reads no environment itself.
func hostFacts() (launch.HostFacts, error) {
	ctxHome, err := paths.HomeConfigDir()
	if err != nil {
		return launch.HostFacts{}, err
	}
	home := filepath.Dir(ctxHome)
	binary, err := os.Executable()
	if err != nil {
		return launch.HostFacts{}, fmt.Errorf("ctxloom binary: %w", err)
	}
	return launch.HostFacts{Home: home, CtxloomHome: ctxHome, Binary: binary}, nil
}

// assembler implements launch.Assembler over the ONE package: Assemble
// assembles it (AssemblePackage → composite.Assemble, once) and the
// resolver reads everything a launch delivers off that Package. pipe is a
// test seam: a pre-configured process stage in place of the gated exposure
// one.
type assembler struct {
	pipe *bundles.Pipeline
	// preview composes the same package for a --dry-run, at the same
	// severity, and delivers no surfaces from it.
	preview bool
	// engines resolves a label's engine for its request-borne environment.
	engines engine.Registry
}

func (a *assembler) Assemble(ctx context.Context, snap *config.Snapshot, sel launch.Selection) (composite.Package, error) {
	req := PackageRequest{Profiles: sel.Profiles, Fragments: sel.Fragments, Tags: sel.Tags, Mail: sel.Mail, Pipeline: a.pipe}
	pkg, err := AssemblePackage(ctx, snap.Config, req)
	if err != nil {
		return composite.Package{}, fmt.Errorf("assemble context: %w", err)
	}
	if err := refuseEmptySelection(req, contextResultOf(pkg)); err != nil {
		return composite.Package{}, err
	}
	return pkg, nil
}

// Index is the generation's catalog (bound on its Config, so a caller that
// wrapped a bare Config still answers) as the runner's search surface.
func (*assembler) Index(_ context.Context, snap *config.Snapshot) (composite.Index, error) {
	return composite.IndexOf(snap.Config.Catalog())
}

// PreviewAssembler is the --dry-run assembler: the real context composition
// (what the preview shows) over a surfaces port that delivers nothing. The
// composition's findings keep their severity — a preview is refused exactly
// where a run would be, and --degraded previews past it exactly as it would
// launch past it — so the preview never renders a setup the run would
// refuse.
func PreviewAssembler(reg engine.Registry) launch.Assembler {
	return &assembler{preview: true, engines: reg}
}

// LabelEnv is the labeled entry's own request-borne environment.
func (a *assembler) LabelEnv(snap *config.Snapshot, label string) map[string]string {
	return MockControlFor(a.engines, snap.Config, label)
}

// Cells implements launch.Cells: it commits around a delegated child's
// worktree cell (parentCommit), has isolation prepare the run's
// Environment and hands the cell exactly that environment's outcome. A
// requested boundary that could not be provided is refused here, typed.
//
// There is no runtime branch here, and there must not be one: where a root
// lands, what is mounted and how the runner starts are the Environment's,
// and a cell reads them off it the same way for every runtime.
type Cells struct {
	cfg  *config.Config
	mode strictness.Mode // the gate's posture: which isolation findings refuse the member
	// engines resolves the member's engine by name.
	engines engine.Registry
	// preview prepares nothing on disk (isolation.Preview): the --dry-run
	// cell, which relocates and probes but creates no checkout, scratch or
	// session home.
	preview bool
	// Git overrides the git seam the parent snapshot uses (nil selects the
	// real binary).
	Git git.Git
}

// PreviewCells is the --dry-run cells adapter: the same relocation a run
// gets, over an environment that creates nothing and starts nothing, so a
// preview routes over the roots a real cell would present — and a container
// preview shows the runtime and reach the run would get (Q5). It refuses
// nothing: what a run's cell would refuse on is left recorded for the
// caller's gate.
func PreviewCells(f LaunchFacts) launch.Cells {
	return Cells{mode: f.Mode, engines: f.Engines, preview: true}
}

// EnvironmentOf reads the prepared environment back off a cell this adapter
// made. ok is false for a cell prepared elsewhere (a test double).
func EnvironmentOf(cell launch.Cell) (isolation.Environment, bool) {
	env, ok := cell.Handle.(isolation.Environment)
	return env, ok
}

// environmentSeam is how the cells adapter reaches isolation. The request
// rides beside the Spec only for a test's stand-in, which keys its answer on
// the member (the Spec is opaque); isolation is handed the Spec alone.
type environmentSeam func(ctx context.Context, req launch.CellRequest, s isolation.Spec) (isolation.Environment, error)

// prepareEnvironment and previewEnvironment are the cells adapter's seams
// onto isolation — package vars so a test hands back a stand-in environment
// (or simulates a container degrade, which records ClassIsolation findings)
// without probing the real host's container runtimes.
var (
	prepareEnvironment environmentSeam = func(ctx context.Context, _ launch.CellRequest, s isolation.Spec) (isolation.Environment, error) {
		return isolation.Prepare(ctx, s)
	}
	previewEnvironment environmentSeam = func(ctx context.Context, _ launch.CellRequest, s isolation.Spec) (isolation.Environment, error) {
		return isolation.Preview(ctx, s), nil
	}
)

// Prepare implements launch.Cells.
func (c Cells) Prepare(ctx context.Context, req launch.CellRequest) (launch.Cell, error) {
	harp := req.Identity.Harp
	commits := c.parentCommit(req, harp)
	spec, creds, err := c.spec(ctx, req, harp, commits)
	if err != nil {
		return launch.Cell{}, err
	}

	// The fail-loudly cell gate: a container degrade, an unpreparable session
	// home and an unpresentable root each record a ClassIsolation finding;
	// the window is this launch's own, so a concurrent launch's findings never
	// poison it. A preview refuses nothing here: it continues past every
	// finding, which stays on the ledger for the dry run's own gate to list
	// after the plan.
	mark := strictness.Checkpoint()
	commits.opened(mark)
	env, err := c.environment(ctx, req, spec)
	found := strictness.Since(mark)
	strictness.Close(mark)
	if err != nil {
		// No environment: a child branch carved on the way is the launch's
		// to remove, and the parent was never touched.
		commits.abandoned()
	} else if commits != nil {
		env = parentCommitEnvironment{Environment: env, commits: commits}
	}
	if cerr := commits.refusal(); cerr != nil {
		// The parent snapshot could not be made: returned as it was
		// phrased, since the runtime is not what failed.
		return launch.Cell{}, cerr
	}
	if errors.Is(err, engine.ErrNoCredential) || errors.Is(err, launch.ErrWorkspaceUnadoptable) {
		// A credential store the run cannot have (missing, or one no
		// container can reach) is the credential's refusal, and a checkout
		// path the agent cannot adopt is the workspace's, each with its own
		// remedy; the runtime is not what failed.
		return launch.Cell{}, err
	}
	if err != nil {
		return launch.Cell{}, fmt.Errorf("%w: %w", launch.ErrRuntimeUnavailable, err)
	}
	if gerr := isolationGateErr(c.mode, found); gerr != nil && !c.preview {
		_ = env.Cleanup()
		return launch.Cell{}, fmt.Errorf("%w: %w", launch.ErrRuntimeUnavailable, gerr)
	}
	placement := env.Placement()
	return launch.Cell{
		Placement:             placement,
		HomeMode:              req.HomeMode,
		Listen:                env.Listen(),
		Credential:            creds.Source(req.Engine.Root().Name),
		CredentialFingerprint: creds.Fingerprint(),
		SecretsFile:           env.SecretsFile(),
		Cleanup:               env.Cleanup,
		Handle:                env,
	}, nil
}

// spec is the isolation Spec the request's cell is prepared from, with the
// credentials its agent authenticates with.
//
// commits, when the cell is a delegated child's worktree (parentCommit),
// snapshots the parent and names the snapshot as the commit a NEW checkout
// is carved from (isolation.SpecBuilder.BeforeCheckout), and commits the
// child's checkout when it is torn down
// (isolation.SpecBuilder.CommitOnTeardown); nil for neither.
func (c Cells) spec(ctx context.Context, req launch.CellRequest, harp string, commits *parentCommit) (isolation.Spec, engine.Credentials, error) {
	// The resolver settled the home mode from the binding's declaration;
	// re-parsed here through the vocabulary's own parser so the cell never
	// asserts a spelling it did not check.
	homeMode, err := agents.ParseHomeMode(string(req.HomeMode))
	if err != nil {
		return isolation.Spec{}, engine.Credentials{}, err
	}
	creds, err := c.runCredentials(req)
	if err != nil {
		return isolation.Spec{}, engine.Credentials{}, err
	}
	b := isolation.NewSpec(req.Axes, req.Engine).
		Project(req.ProjectRoot).
		Session(req.SessionDir, isolation.SessionState{Harp: harp, ProjectID: req.Identity.Project}).
		Image(req.Image).
		Home(homeMode).
		Credentials(creds).
		EnvHost(req.EnvHost)
	if commits != nil {
		b = b.BeforeCheckout(commits.beforeCheckout).CommitOnTeardown()
	}
	spec, err := b.Build()
	return spec, creds, err
}

// runCredentials is what the request's agent authenticates with
// (resolveRunAuth), in the mode the resolver settled (launch.RunAuth) — before the
// environment exists, so a missing credential refuses before anything is
// built. A preview resolves them with every value redacted (previewRunAuth),
// so the environment sees the stores the run would share; a refusal the run
// would return is recorded for the dry run's gate and the preview carries on
// without credentials.
func (c Cells) runCredentials(req launch.CellRequest) (engine.Credentials, error) {
	in := runAuth{Backend: string(req.Engine.Root().Name), Mode: req.Auth}
	if !c.preview {
		return resolveRunAuth(c.engines, in)
	}
	creds, err := previewRunAuth(c.engines, in)
	if err != nil {
		remedy, _ := clifmt.RemedyOf(err)
		strictness.FailAlways(report.KindIsolation, remedy, "refusing to run: %v", err)
		return engine.Credentials{}, nil
	}
	return creds, nil
}

// environment is the prepared environment, or the preview's.
func (c Cells) environment(ctx context.Context, req launch.CellRequest, spec isolation.Spec) (isolation.Environment, error) {
	if c.preview {
		return previewEnvironment(ctx, req, spec)
	}
	return prepareEnvironment(ctx, req, spec)
}

// parentCommit is the commit on each side of a delegated child's worktree
// run (the rationale is with snapshotParentTree): the parent's uncommitted
// and untracked state is snapshotted OFF-BRANCH before a NEW checkout is
// carved, the checkout is carved from the snapshot, and the parent's branch
// advances to it only once the child is up; the child's checkout commits
// whatever it holds uncommitted on its own branch when the cell is torn
// down, however the run ended. A delegated child's concern only: the
// originator who asked for a worktree is at the terminal with the tree in
// front of them.
//
// Its three moments:
//
//   - beforeCheckout, isolation's BeforeCheckout hook: runs only once
//     isolation is about to carve the checkout, after the container gate
//     and the occupied-path check, and only while no isolation finding that
//     refuses the run has been recorded in this launch's window
//     (isolationGateErr over the findings so far). It never runs for a
//     resume that re-enters its own checkout or its own branch. It builds
//     the snapshot and names it as the carve's base; nothing on the
//     parent's side changes.
//   - started, when the cell's environment Start (or Interactive) returns:
//     the child is UP — its runner started where it runs, a container
//     observed running — so no workspace, isolation or container-start
//     failure can follow. The parent's branch advances to the snapshot by a
//     compare-and-swap from the HEAD the snapshot sits on, and the parent's
//     index is reset to it. A branch that moved meanwhile is left alone,
//     and said so loudly.
//   - abandoned, when the cell is torn down (or never built) before it was
//     up: the child's freshly carved branch — the only ref holding the
//     snapshot — is deleted, by a compare-and-swap from the commit it was
//     carved at. The parent was never touched.
//
// A nil parentCommit is a launch none of this governs; its methods are
// nil-safe.
type parentCommit struct {
	git     git.Git
	project string
	harp    string
	mode    strictness.Mode
	// mark is the launch's findings window, opened just before the
	// environment is prepared (opened); the hook reads the findings so far.
	mark strictness.Mark
	// err is the parent snapshot's own refusal.
	err error

	mu sync.Mutex
	// snap is the parent's snapshot, zero when nothing was snapshotted.
	snap parentSnapshot
	// child and base are the branch carved for the child and the commit it
	// was carved at; zero until a checkout is carved, and cleared once the
	// branch is deleted. base is "" when it could not be resolved, which
	// leaves the branch in place.
	child git.Branch
	base  string
	// up is set once the child's environment started.
	up bool
}

// parentCommit is the request's commits, or nil when the cell is not a
// delegated child's worktree.
func (c Cells) parentCommit(req launch.CellRequest, harp string) *parentCommit {
	if c.preview || req.Axes.Workspace != launch.WorkspaceWorktree || !req.Identity.IsChild() {
		return nil
	}
	gitClient := c.Git
	if gitClient == nil {
		gitClient = git.NewExec()
	}
	return &parentCommit{git: gitClient, project: req.ProjectRoot, harp: harp, mode: c.mode}
}

// opened records the launch's findings window.
func (p *parentCommit) opened(mark strictness.Mark) {
	if p != nil {
		p.mark = mark
	}
}

// beforeCheckout snapshots the parent and returns the commit the child's
// new branch is carved from: the snapshot, or the parent's HEAD when there
// is nothing to snapshot. A launch already refused is refused here, before
// anything is made: that refusal is the isolation gate's, which Prepare
// reports.
func (p *parentCommit) beforeCheckout(ctx context.Context, child git.Branch) (string, error) {
	if gerr := isolationGateErr(p.mode, strictness.Since(p.mark)); gerr != nil {
		return "", gerr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	snap, err := snapshotParentTree(ctx, p.git, p.project, p.harp)
	if err != nil {
		p.err = err
		return "", err
	}
	p.snap = snap
	base := snap.commit
	if base == "" {
		// Nothing to snapshot: the child is carved from HEAD, pinned here so
		// an abandoned launch deletes exactly the branch it carved. Best
		// effort, like the dirty probe: unresolved, it carves from "HEAD".
		base, _ = p.git.HeadSHA(ctx, p.project)
	}
	p.child, p.base = child, base
	return base, nil
}

// upTimeout bounds the parent-side git work at the child's start and its
// abandonment, which run on a context of their own: the launch's may end the
// moment the runner is up.
const upTimeout = 30 * time.Second

// started is the child up: the parent's branch advances to the snapshot,
// unless it moved since the snapshot was taken, which is said loudly and
// left alone. Once.
func (p *parentCommit) started(ctx context.Context) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.up {
		return
	}
	p.up = true
	if p.snap.commit == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upTimeout)
	defer cancel()
	s := p.snap
	err := p.git.AdvanceBranch(ctx, p.project, s.branch, s.commit, s.head)
	switch {
	case err == nil:
	case errors.Is(err, git.ErrRefMoved):
		clidiag.Warn("ctxloom", "agent_run: delegated agent %q started from snapshot %s of %s, but branch %q moved while it was launching (%v), so it was NOT advanced to the snapshot: %s is not on %q, and %s still holds those changes uncommitted. The agent's work merges back from its own branch as usual.",
			p.harp, shortSHA(s.commit), p.project, s.branch, err, shortSHA(s.commit), s.branch, p.project)
	default:
		clidiag.Warn("ctxloom", "agent_run: delegated agent %q started from snapshot %s of %s, but advancing branch %q to it failed: %v. The snapshot may not be on %q; the agent's work merges back from its own branch as usual.",
			p.harp, shortSHA(s.commit), p.project, s.branch, err, s.branch)
	}
}

// abandoned is the cell gone before the child was up: the branch carved for
// it is deleted — only while it is still at the commit it was carved at and
// no checkout holds it. Once.
func (p *parentCommit) abandoned() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.up || p.child.IsZero() || p.base == "" {
		return
	}
	child, base := p.child, p.base
	p.child, p.base = git.Branch{}, ""
	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()
	if exists, err := p.git.BranchExists(ctx, p.project, child); err != nil || !exists {
		return
	}
	if err := p.git.DeleteBranchAt(ctx, p.project, child, base); err != nil {
		clidiag.Warn("ctxloom", "agent_run: delegated agent %q never started, but its branch %q could not be removed: %v", p.harp, child, err)
	}
}

// refusal is the parent snapshot's own refusal, nil for none.
func (p *parentCommit) refusal() error {
	if p == nil {
		return nil
	}
	return p.err
}

// parentCommitEnvironment is a delegated child's worktree environment with
// its parentCommit's moments bound to it: a Start or Interactive that
// returns is the child up (started), and a Cleanup is the cell's end
// (abandoned, a no-op once the child was up). Every start path reaches the
// environment through the cell's Handle (EnvironmentOf), so none bypasses it.
type parentCommitEnvironment struct {
	isolation.Environment
	commits *parentCommit
}

func (e parentCommitEnvironment) Start(ctx context.Context, r isolation.RunnerRequest) (*isolation.RunnerHandle, error) {
	h, err := e.Environment.Start(ctx, r)
	if err == nil {
		e.commits.started(ctx)
	}
	return h, err
}

func (e parentCommitEnvironment) Interactive(ctx context.Context, r isolation.RunnerRequest) (isolation.Interactive, error) {
	in, err := e.Environment.Interactive(ctx, r)
	if err == nil {
		e.commits.started(ctx)
	}
	return in, err
}

func (e parentCommitEnvironment) Cleanup() error {
	err := e.Environment.Cleanup()
	e.commits.abandoned()
	return err
}

// endpointMinter mints the session's MCP endpoint on the host: a reserved
// loopback port and a fresh bearer. Nothing binds it yet; it is carried on
// the launch only, and each launch, a resume included, mints its own.
type endpointMinter struct{}

func (endpointMinter) MintMCP(_ context.Context, _ sessions.Identity, _ launch.Axes) (sessions.Endpoint, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return sessions.Endpoint{}, fmt.Errorf("mint MCP endpoint: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return sessions.Endpoint{}, fmt.Errorf("mint MCP endpoint credential: %w", err)
	}
	return sessions.Endpoint{URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/mcp", Credential: hex.EncodeToString(b[:])}, nil
}
