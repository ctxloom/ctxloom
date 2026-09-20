package operations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// This file is the trunk every host-side launch enters: StartRun mints the
// identity and resolves the launch through launch.Resolve, with the ports
// Resolve needs composed once by LaunchDeps. The cells adapter here is the
// ONE place the three launch paths used to each prepare a workspace, bind
// the engine home and settle the dirty parent tree.

// StartRun mints the session identity for seed and resolves src through the
// one resolver. The caller starts the transport it owns from the Launch and
// ends the session (EndSession) when the run is over; a launch that does not
// resolve ends its own session here so no harp is left half-minted.
func StartRun(ctx context.Context, deps launch.Deps, seed sessions.Seed, src launch.Source) (launch.Launch, error) {
	id, err := MintIdentity(deps.Sessions, seed)
	if err != nil {
		return launch.Launch{}, err
	}
	src.Identity = id
	if deps.ClaimCheck == nil {
		deps = ForSession(deps, id.Harp)
	}
	l, err := launch.Resolve(ctx, deps, src)
	if err != nil {
		if eerr := EndSessionIn(deps.Sessions, id.Harp, time.Now()); eerr != nil {
			clidiag.Warn("ctxloom", "session %s: end after a refused launch: %v", id.Harp, eerr)
		}
		return launch.Launch{}, err
	}
	return l, nil
}

// MintIdentity is THE mint on the host: the harp assigned in the store (its
// directory and sidecar), the liveness lock held by this process, the
// identity returned. The engine is not known here — Resolve decides it and
// records it (Store.BindEngine).
func MintIdentity(store sessions.Store, seed sessions.Seed) (sessions.Identity, error) {
	entry, err := store.AssignHarp(seed.ProjectDir, seed.Engine)
	if err != nil {
		return sessions.Identity{}, fmt.Errorf("session naming failed, refusing to run: %w", err)
	}
	// THIS PROCESS OWNS THE SESSION FROM HERE: hold its liveness lock until
	// EndSession. A failed hold leaves NO lock file, so the harp reads
	// Indeterminate — never reclaimed — rather than Dead.
	if herr := sessionlock.Hold(entry.HarpName); herr != nil {
		clidiag.Warn("ctxloom", "session %s: cannot hold its liveness lock, so its data will never be reaped as crashed: %v", entry.HarpName, herr)
	}
	return sessions.Identity{Harp: entry.HarpName, Depth: seed.Depth, OneShot: seed.OneShot, Project: seed.ProjectID}, nil
}

// LaunchDeps composes the resolver's ports over this process's published
// generation.
func (a *App) LaunchDeps(ctx context.Context) (launch.Deps, error) {
	snap, err := a.Snapshot(ctx)
	if err != nil {
		return launch.Deps{}, err
	}
	return LaunchDepsFor(snap, a.Strictness)
}

// LaunchDepsFor composes the resolver's ports over one generation: the
// composed engines, the assembler and cells adapters, the endpoint minter,
// the session store and the host facts. A caller holding only the
// generation's Config (the compactor, the trigger evaluator) wraps it in a
// Snapshot; Resolve reads the Config and nothing else off it.
func LaunchDepsFor(snap *config.Snapshot, mode strictness.Mode) (launch.Deps, error) {
	store, err := openSessions()
	if err != nil {
		return launch.Deps{}, err
	}
	host, err := hostFacts()
	if err != nil {
		return launch.Deps{}, err
	}
	return launch.Deps{
		Snapshot:  snap,
		Engines:   backends.Engines(),
		Assembler: &assembler{},
		Cells:     Cells{cfg: snap.Config, mode: mode},
		Endpoints: endpointMinter{},
		Sessions:  store,
		Inline:    composite.Inline{Max: composite.DefaultInlineMax},
		InlineMax: composite.DefaultInlineMax,
		Host:      host,
		Reporter:  mode.Sink(),
	}, nil
}

// Opened is a launch's package as the local launcher reads it: the decoded
// package and the managed payload today's writers deliver from it, with the
// binding's delivery preference validated against the engine.
type Opened struct {
	Package composite.Package
	Managed *agent.ManagedConfig
}

// OpenLaunch is the in-process consumer of the carrier (launch.Open) plus
// the projection the plugin arm still hands its writers: the same package
// and the same payload the runner builds for a delegated launch, so the two
// deliver one set.
func OpenLaunch(ctx context.Context, deps launch.Deps, l launch.Launch) (Opened, error) {
	pkg, err := launch.Open(ctx, deps, l)
	if err != nil {
		return Opened{}, fmt.Errorf("open the launch's package: %w", err)
	}
	managed := agent.ManagedConfigFor(ManagedSurfacesOf(pkg), l.Exports)
	agent.PreferSurfaces(report.To(deps.Reporter), managed, string(l.Engine), pkg.Selection.Preference, ResolveAgentSurfaces)
	return Opened{Package: pkg, Managed: managed}, nil
}

// ForSession roots the claim check at the minted session: the package store
// is the session dir (<harp>/persist/package), so it exists only once the
// harp does. StartRun composes it after its mint unless the caller already
// chose a claim check (a preview keeps its claims in memory).
func ForSession(deps launch.Deps, harp string) launch.Deps {
	deps.ClaimCheck = composite.ClaimCheck{Store: fsstore.PackageStore{Root: filepath.Join(deps.Host.CtxloomHome, paths.SessionsDir), Harp: harp}}
	return deps
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
}

func (a *assembler) Assemble(ctx context.Context, snap *config.Snapshot, sel launch.Selection) (composite.Package, error) {
	req := PackageRequest{Profiles: sel.Profiles, Fragments: sel.Fragments, Tags: sel.Tags, WorkDir: sel.WorkDir, Pipeline: a.pipe}
	pkg, err := AssemblePackage(ctx, snap.Config, req)
	if err != nil {
		return composite.Package{}, fmt.Errorf("assemble context: %w", err)
	}
	if err := refuseEmptySelection(req, contextResultOf(pkg)); err != nil {
		return composite.Package{}, err
	}
	// A withheld executable is reported, content-free, never silently.
	WarnWithheldBy(snap.Config.ExecutableTrustGate())
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
func PreviewAssembler() launch.Assembler { return &assembler{preview: true} }

// LabelEnv is the labeled entry's own request-borne environment.
func (*assembler) LabelEnv(snap *config.Snapshot, label string) map[string]string {
	return MockControlFor(snap.Config, label)
}

// Cells implements launch.Cells: it settles the dirty parent tree for a
// delegated child's worktree cell, prepares the workspace through
// isolation's degrade chain, binds the engine's controlled home and reports
// the cell's roots and env. A requested boundary that could not be provided
// is refused here, typed.
type Cells struct {
	cfg  *config.Config
	mode strictness.Mode // the gate's posture: which isolation findings refuse the member
	// Git overrides the git seam the dirty-parent-tree decision uses (nil
	// selects the real binary).
	Git git.Git
}

// PreparedCell is the handle the cells adapter keeps on a Cell for today's
// transport: the isolation policy that prepared the workspace and the
// workspace itself. TransportOf reads it back.
type PreparedCell struct {
	Policy    isolation.Policy
	Workspace isolation.Workspace
}

// TransportOf reads the prepared transport back off a cell this adapter
// made. ok is false for a cell prepared elsewhere (a test double).
func TransportOf(cell launch.Cell) (PreparedCell, bool) {
	p, ok := cell.Handle.(PreparedCell)
	return p, ok
}

// prepareIsolation is the cells adapter's seam onto isolation.Prepare — a
// package var so a test simulates a container degrade (which records
// ClassIsolation findings) or hands back a stand-in workspace without
// probing the real host's container runtimes.
var prepareIsolation = isolation.Prepare

// Prepare implements launch.Cells.
func (c Cells) Prepare(ctx context.Context, req launch.CellRequest) (launch.Cell, error) {
	backend := string(req.Engine.Root().Name)
	harp := req.Identity.Harp

	var (
		gitClient   git.Git
		pendingCopy *copySnapshot
	)
	// The dirty-tree handler is a DELEGATED spawn's concern (its rationale
	// is with handleDirtyParentTree): the originator who asked for a
	// worktree is at the terminal with the tree in front of them, and is
	// not gated on it. The handler itself arrives settled by the resolver.
	if req.Axes.Workspace == launch.WorkspaceWorktree && req.Identity.IsChild() {
		gitClient = c.Git
		if gitClient == nil {
			gitClient = git.NewExec()
		}
		outcome, err := handleDirtyParentTree(ctx, c.cfg, gitClient, req.ProjectRoot, harp, req.DirtyTree)
		if err != nil {
			return launch.Cell{}, err
		}
		pendingCopy = outcome.copy
	}

	// The fail-loudly cell gate: a container degrade inside Prepare records
	// a ClassIsolation finding; the window is this launch's own, so a
	// concurrent launch's findings never poison it.
	// The resolver settled the home mode from the binding's declaration;
	// re-parsed here through the vocabulary's own parser so the cell never
	// asserts a spelling it did not check.
	homeMode, err := agents.ParseHomeMode(string(req.HomeMode))
	if err != nil {
		return launch.Cell{}, err
	}
	mark := strictness.Checkpoint()
	policy, ws := prepareIsolation(ctx, req.Axes, backend, req.Image, req.ProjectRoot, harp, isolation.SessionStateFromEnv(req.Env))
	env := isolation.WorkspaceEnv(ws)
	home := BindAgentHome(ws, InTreeAgentHome{
		Backend:  backend,
		WorkDir:  req.ProjectRoot,
		Cwd:      ws.Dir(),
		Harp:     harp,
		HomeMode: homeMode,
	})
	found := strictness.Since(mark)
	strictness.Close(mark)
	if gerr := isolationGateErr(c.mode, found); gerr != nil {
		_ = ws.Cleanup()
		return launch.Cell{}, fmt.Errorf("%w: %v", launch.ErrRuntimeUnavailable, gerr)
	}
	if pendingCopy != nil {
		if err := applyCopySnapshot(ctx, gitClient, ws.Dir(), pendingCopy); err != nil {
			_ = ws.Cleanup()
			return launch.Cell{}, err
		}
	}

	roots := present.Paths{
		ProjectRoot: present.Root{Host: ws.Dir()},
		CtxloomHome: present.Root{Host: req.Host.CtxloomHome},
		Scratch:     present.Root{Host: req.SessionDir},
	}
	cell := launch.Cell{
		Workspace: ws.Dir(),
		Env:       env,
		Cleanup:   ws.Cleanup,
		Handle:    PreparedCell{Policy: policy, Workspace: ws},
	}
	if home.Absent == "" {
		roots.EngineHome = present.Root{Host: home.Root.Host}
		if cell.Env == nil {
			cell.Env = map[string]string{}
		}
		maps.Copy(cell.Env, home.Env)
		for k, v := range home.Env {
			cell.Home = append(cell.Home, engine.HomeBinding{Var: k, Path: v})
		}
	}
	if isolation.IsContainerPolicyName(policy.Name()) {
		advice := present.Containerize{}
		if home.Mount != nil {
			advice.EngineHome = home.Mount.TargetDir
		}
		cell.Paths = advice.Apply(roots)
		cell.Container = &launch.ContainerCell{
			Runtime: req.Axes.Runtime,
			Mounts:  cell.Paths.Mounts(),
			Home:    home.Root.Engine,
		}
	} else {
		cell.Paths = present.OnHost(roots)
	}
	return cell, nil
}

// endpointMinter mints the session's MCP endpoint on the host: a reserved
// loopback port and a fresh bearer. Nothing binds it yet; it is carried on
// the launch and recorded on the session so a resume reuses it.
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
