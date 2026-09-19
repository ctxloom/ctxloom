package launch

import (
	"context"
	"errors"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// HostFacts are the originator's host-side facts, decoded ONCE at the
// composition root and passed down: the real home (credential seeding), the
// ctxloom home, and the ctxloom binary. A container never receives a host
// path as if universal.
type HostFacts struct {
	Home        string
	CtxloomHome string
	Binary      string
}

// Deps are the ports Resolve needs; every one built once at the composition
// root. The catalog and the trust gate come from the ONE Snapshot the
// operation captured. Resolve reads no file and no env.
type Deps struct {
	Snapshot *config.Snapshot
	Engines  engine.Registry
	// Assembler composes the package. It is a PORT here because the core
	// package assembler (composite.Assemble, with its Encode and the two
	// carrier transports) is not born yet; operations implements it over
	// today's profile assembly and managed-surface composition. When
	// composite.Package lands this port and Package retire for it.
	Assembler Assembler
	Cells     Cells
	Endpoints EndpointMinter
	Sessions  sessions.Store
	Host      HostFacts
}

// Assembler is the package-assembly port: the profile set to its composed
// context, then the managed surfaces for the engine the label resolved to.
// Two calls because the label depends on what the profiles declared.
type Assembler interface {
	Assemble(ctx context.Context, snap *config.Snapshot, sel Selection) (Assembled, error)
	Surfaces(ctx context.Context, snap *config.Snapshot, eng engine.Name, projectRoot string, profiles []string, preference map[string]string) (Surfaces, error)
	// LabelEnv is the label's request-borne environment: the engine
	// passthrough the labeled entry's own config carries (the mock's
	// test-control map; every real engine's environment is ambient and
	// carries none). It rides Launch.Env beneath the caller's own.
	LabelEnv(snap *config.Snapshot, label string) map[string]string
}

// Selection is what Assemble composes: the profile set, plus the explicit
// arm's named fragments and tag matches.
type Selection struct {
	Profiles  []string
	Fragments []string
	Tags      []string
}

// Assembled is what a profile set composed to.
type Assembled struct {
	Context    string
	Profiles   []string
	Fragments  []string
	ProfileLLM string // the label the profiles declared, if any
}

// Package is the assembled package as it is carried today: the context
// text and the managed surfaces (MCP servers, hooks, commands, skills,
// settings). It stands where composite.Carrier will: encoded and carried by
// size once the composite package exists.
type Package struct {
	Context   string
	Managed   Surfaces
	Profiles  []string
	Fragments []string
}

// Surfaces is the managed-surface payload as today's wire carries it. The
// launch only CARRIES it — from the assembler to the wire codec — and never
// reads it beyond its engine-facing projection, so it is opaque here: its
// type lives with the legacy engine contract this package must not depend
// on. The codec asserts it back to that type; composite.Package retires it.
type Surfaces interface {
	Items() engine.Items
}

// EndpointMinter mints the session's MCP endpoint: loopback URL and bearer.
// Called by Resolve ONCE per harp; the result is bound on the session
// record, so every one-shot turn and every resume reuses it.
type EndpointMinter interface {
	MintMCP(ctx context.Context, id sessions.Identity, axes Axes) (sessions.Endpoint, error)
}

// Cells is the port isolation implements: prepare the cell a launch runs in
// and advise its roots. Called by Resolve exactly once per launch.
type Cells interface {
	Prepare(ctx context.Context, req CellRequest) (Cell, error)
}

// HomeMode is the binding's engine-home policy: keep the home the runtime
// gives the engine, or give it this session's controlled home. It rides
// CellRequest until Engine.Home() is the engine's own declaration.
type HomeMode string

const (
	HomeModeHost    HomeMode = "host"
	HomeModeSession HomeMode = "session"
)

// CellRequest is what the cells adapter is asked for.
type CellRequest struct {
	Axes        Axes
	Engine      engine.Engine // the container and home facts are read here, by the adapter
	Identity    sessions.Identity
	ProjectRoot string
	SessionDir  string
	DirtyTree   DirtyTreeHandler
	Image       ImageConfig
	Host        HostFacts
	Degraded    bool
	HomeMode    HomeMode
	// Env is the run's own environment: the identity carriers the cell's
	// session state is keyed from and the caller's passthrough.
	Env map[string]string
}

// Cell is a prepared place to run. A Cell exists only inside a Launch.
type Cell struct {
	Paths     present.Mapped
	Workspace string
	Env       map[string]string
	Home      []engine.HomeBinding
	Container *ContainerCell
	Cleanup   func() error
	// Handle is what the cells adapter keeps to START a process in this cell
	// under today's transport: opaque to core, read back only by the adapter
	// that made it. It leaves with that transport, when the runner is the one
	// process every cell starts.
	Handle any
}

// ContainerCell is the container half of a cell.
type ContainerCell struct {
	Runtime RuntimeAxis
	Image   string
	Mounts  []present.Mount
	Home    string
}

// Launch is the resolved launch. Immutable once returned. Everything a runner
// needs is here, typed; nothing is re-derived after this value exists.
// Not here, on purpose: OneShot (Identity.OneShot), the attestation (inside
// the package), ReachBack (the runner already holds it from its env).
type Launch struct {
	Identity   sessions.Identity
	Engine     engine.Name
	Label      engine.LabelConfig
	Mode       engine.Mode
	Permission engine.PermissionMode // floored ONCE, here
	Axes       Axes
	Cell       Cell
	Home       []engine.HomeBinding
	Package    Package
	Exports    engine.Exports
	Plan       delivery.Plan
	MCP        sessions.Endpoint // minted per harp in Resolve; the runner BINDS it
	Prompt     string
	Resume     sessions.ResumeRef
	Env        map[string]string // engine passthrough only
}

var (
	ErrNoIdentity           = errors.New("launch: a launch needs a minted identity")
	ErrModeUnsupported      = errors.New("launch: the engine does not declare the requested mode")
	ErrOwnershipMismatch    = errors.New("launch: the binding's container ownership is not available")
	ErrRuntimeUnavailable   = errors.New("launch: the requested runtime is not available")
	ErrNoAgent              = errors.New("launch: the named agent is not found")
	ErrNoEngine             = errors.New("launch: the label names no composed engine")
	ErrPermissionUnhonoured = errors.New("launch: the declared permission posture cannot be honoured")
	ErrContextEmpty         = errors.New("launch: the named profile set assembled to nothing")
)

// Session is the ONLY constructor of the engine-facing projection.
func (l Launch) Session() engine.Session {
	return engine.Session{
		Identity: l.Identity, Label: l.Label, Mode: l.Mode, Permission: l.Permission,
		Roots: l.Cell.Paths.Paths(), WorkDir: l.Cell.Workspace, Home: l.Home, MCP: l.MCP,
		Prompt: l.Prompt, Resume: l.Resume, Env: l.Env,
	}
}

// Discard tears the cell down. Safe on a zero Launch.
func Discard(_ context.Context, l Launch) error {
	if l.Cell.Cleanup == nil {
		return nil
	}
	return l.Cell.Cleanup()
}

// StructuredMode is a readability helper for callers (children and
// one-shots are always Structured).
func StructuredMode() engine.Mode { return engine.Structured }
