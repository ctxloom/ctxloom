package operations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
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
		Host:      host,
	}, nil
}

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
// assembles it (AssemblePackage → composite.Assemble, once) and Surfaces
// projects the engine's managed surfaces off the same Package, so the
// context a run delivers and the surfaces beside it are one assembly. pipe
// is a test seam: a pre-configured process stage in place of the gated
// exposure one.
type assembler struct {
	pipe *bundles.Pipeline
	// preview composes the same package for a --dry-run and delivers no
	// surfaces from it; the package's findings are advisory (PackageRequest.Preview).
	preview bool
	// pkg is the package Assemble assembled, for Surfaces; profiles is the
	// profile set it was assembled for.
	pkg      *composite.Package
	profiles []string
}

func (a *assembler) Assemble(ctx context.Context, snap *config.Snapshot, sel launch.Selection) (launch.Assembled, error) {
	req := PackageRequest{Profiles: sel.Profiles, Fragments: sel.Fragments, Tags: sel.Tags, Pipeline: a.pipe, Preview: a.preview}
	pkg, err := AssemblePackage(ctx, snap.Config, req)
	if err != nil {
		return launch.Assembled{}, fmt.Errorf("assemble context: %w", err)
	}
	res := contextResultOf(pkg)
	if err := refuseEmptySelection(req, res); err != nil {
		return launch.Assembled{}, err
	}
	a.pkg, a.profiles = &pkg, res.Profiles
	return launch.Assembled{Context: res.Context, Profiles: res.Profiles, Fragments: res.FragmentsLoaded, ProfileLLM: res.ProfileLLM}, nil
}

// PreviewAssembler is the --dry-run assembler: the real context composition
// (what the preview shows), its composition findings advisory, over a
// surfaces port that delivers nothing. The preview must render the setup a
// user is diagnosing, not refuse it.
func PreviewAssembler() launch.Assembler { return &assembler{preview: true} }

// LabelEnv is the labeled entry's own request-borne environment.
func (*assembler) LabelEnv(snap *config.Snapshot, label string) map[string]string {
	return MockControlFor(snap.Config, label)
}

// Surfaces projects the managed surfaces for the engine off the package
// Assemble assembled for the same profile set — assembled here only when a
// run selected no context at all; the binding's delivery preference is
// validated against the engine and rides on the payload. A withheld
// executable is reported, content-free, never silently.
func (a *assembler) Surfaces(ctx context.Context, snap *config.Snapshot, eng engine.Name, projectRoot string, profiles []string, preference map[string]string) (launch.Surfaces, error) {
	if a.preview {
		// A preview delivers no surfaces, so none are projected for it; the
		// context it shows is still the one assembly a run would deliver.
		return nil, nil
	}
	pkg := a.pkg
	if pkg == nil || !slices.Equal(a.profiles, profiles) {
		assembled, err := AssemblePackage(ctx, snap.Config, PackageRequest{Profiles: profiles, WorkDir: projectRoot, Pipeline: a.pipe})
		if err != nil {
			return nil, err
		}
		pkg = &assembled
	}
	managed, err := ManagedConfigOf(*pkg, string(eng))
	if err != nil {
		return nil, err
	}
	WarnWithheldBy(snap.Config.ExecutableTrustGate())
	if len(preference) > 0 {
		surfaces, err := ResolveAgentSurfaces(string(eng), preference)
		if err != nil {
			clidiag.Warn("ctxloom", "delivery preference: %v — using %s's default delivery", err, eng)
		} else {
			managed.Surfaces = surfaces
		}
	}
	return managed, nil
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
	// The cell's teardown, in the order the run's end needs: the home is
	// released FIRST — its credential replicator stops writing into the
	// instance — and only then is the workspace torn down. Stopping the
	// replicator at any earlier point would leave a live engine on a token
	// the host has since rotated; leaving it running past this point leaks
	// a watcher into the coordinator for every launch.
	cleanup := func() error {
		return errors.Join(home.Release(), ws.Cleanup())
	}
	found := strictness.Since(mark)
	strictness.Close(mark)
	if gerr := isolationGateErr(c.mode, found); gerr != nil {
		_ = cleanup()
		return launch.Cell{}, fmt.Errorf("%w: %v", launch.ErrRuntimeUnavailable, gerr)
	}
	if pendingCopy != nil {
		if err := applyCopySnapshot(ctx, gitClient, ws.Dir(), pendingCopy); err != nil {
			_ = cleanup()
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
		Cleanup:   cleanup,
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
