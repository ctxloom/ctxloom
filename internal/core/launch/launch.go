package launch

import (
	"context"
	"errors"
	"maps"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// HostFacts are the originator's host-side facts, decoded ONCE at the
// composition root and passed down: the real home, the ctxloom home, and the
// ctxloom binary. A container never receives a host
// path as if universal.
type HostFacts struct {
	Home        string
	CtxloomHome string
	Binary      string
}

// Deps are the ports Resolve needs; every one built once at the composition
// root. The catalog and the trust gate come from the ONE Snapshot the
// operation captured. Resolve reads no file and no env. Inline and ClaimCheck
// are the two package transports; InlineMax is the size at which Resolve
// switches from one to the other.
type Deps struct {
	Snapshot *config.Snapshot
	Engines  engine.Registry
	// Assembler composes the ONE package a launch delivers (composite.Assemble
	// behind it). It is a port because the inputs Assemble takes — the
	// builtin injections, the config-level hooks and servers, the trust
	// gate's process stage — are resolved by the application service that
	// owns the generation; Resolve reads the Package and nothing else.
	Assembler Assembler
	Cells     Cells
	Endpoints EndpointMinter
	Sessions  sessions.Store
	// ClaimCheck is rooted at THIS launch's session dir (the store is per
	// session by construction), so it is set after the identity is minted —
	// ForSession, over the composed SessionClaims — unless the caller already
	// chose one (a preview keeps its claims in memory); Inline and InlineMax
	// are process-wide.
	Inline        composite.Transport
	ClaimCheck    composite.Transport
	SessionClaims SessionClaims
	InlineMax     int
	Host          HostFacts
	// Reporter receives the diagnostics opening a launch raises (the delivery
	// preference the engine cannot honour, the packages a writer skips); the
	// composition chooses the sink. Nil discards.
	Reporter report.Sink
}

// SessionClaims constructs the claim-check transport rooted at one session's
// directory under sessionsRoot. It is a constructor, not a store, because
// the store exists only once the harp is minted; the composition root
// supplies the adapter-backed one.
type SessionClaims func(sessionsRoot, harp string) composite.Transport

// ForSession roots the claim check at the minted session harp, under the
// ctxloom home's sessions directory. A Deps composed without SessionClaims
// is a composition error with no store to fall back on, so it panics rather
// than launching with claims that land nowhere.
func (d Deps) ForSession(harp string) Deps {
	if d.SessionClaims == nil {
		panic("launch: Deps.ForSession: no SessionClaims composed; the composition root must supply the session claim store")
	}
	d.ClaimCheck = d.SessionClaims(filepath.Join(d.Host.CtxloomHome, paths.SessionsDir), harp)
	return d
}

// Assembler is the package-assembly port: the profile set and the explicit
// arm to the one composite.Package every consumer of this launch reads.
type Assembler interface {
	Assemble(ctx context.Context, snap *config.Snapshot, sel Selection) (composite.Package, error)
	// Index is the catalog the package was assembled from — refs, kinds and
	// descriptions, not bytes — so the runner serves search_library and the
	// ctxloom:// resources with no config owner. The catalog is the
	// generation's, resolved by the service that owns it.
	Index(ctx context.Context, snap *config.Snapshot) (composite.Index, error)
	// LabelEnv is the label's request-borne environment: the engine
	// passthrough the labeled entry's own config carries (the mock's
	// test-control map; every real engine's environment is ambient and
	// carries none). It rides Launch.Env beneath the caller's own.
	LabelEnv(snap *config.Snapshot, label string) map[string]string
}

// Selection is what Assemble composes: the profile set, plus the explicit
// arm's named fragments and tag matches, for a session of Mode.
type Selection struct {
	Profiles  []string
	Fragments []string
	Tags      []string
	// Mail is who reads the session's spool (mailReaderOf): ctxloom's own
	// managed hooks declare the turn-start mail reader only for MailByHook.
	Mail sessions.MailReader
}

// EndpointMinter mints the session's MCP endpoint: loopback URL and bearer.
// Called by Resolve for EVERY launch, a resume included, and by
// RebindEndpoint; nothing persists the result, so a bearer dies with the
// launch that minted it.
type EndpointMinter interface {
	MintMCP(ctx context.Context, id sessions.Identity, axes Axes) (sessions.Endpoint, error)
}

// Cells is the port isolation implements: prepare the cell a launch runs in
// and advise its roots. Called by Resolve exactly once per launch.
type Cells interface {
	Prepare(ctx context.Context, req CellRequest) (Cell, error)
}

// SessionHome is the ONE rule placing a session's home for an engine:
// <sessionDir>/home/<leaf>, where the leaf is the engine's declared home
// subdir when it relocates one and its name when it relocates nothing. Every
// engine gets one, the non-relocating ones included, so a session-private
// delivery always has a root that is not the project. ok is false for
// agents.HomeModeHost — that binding runs the engine on the home its runtime
// gives it, which is not ours to deliver into — and for a run with no session
// dir, which has nowhere to put one. The zero agents.HomeMode is the parser's
// default, the session.
//
// The leaf is the DECLARED subdir rather than the engine name because the
// engine's own home var points at <session home>/<subdir> (engine.HomeVar);
// naming the directory any other way would split the root from the var.
func SessionHome(sessionDir string, eng engine.Engine, m agents.HomeMode) (dir string, ok bool) {
	if m == agents.HomeModeHost || sessionDir == "" {
		return "", false
	}
	leaf := string(eng.Root().Name)
	if home := eng.Home(); home.Relocates() {
		leaf = home.Vars[0].Subdir
	}
	return filepath.Join(sessionDir, paths.SessionEngineHomesDirName, leaf), true
}

// NativeHome is where a session keeps eng's NATIVE conversation history:
// <sessionDir>/native/<leaf>, the session home's leaf (SessionHome) at the
// same depth, so the one relative link the home holds
// (engine.HomeSpec.TranscriptStoreRel) resolves wherever the two are
// siblings — on the host, and in a container that mounts them side by side.
// ok is false when there is no session home to link from, or the engine keeps
// no history store.
func NativeHome(sessionDir string, eng engine.Engine, m agents.HomeMode) (dir string, ok bool) {
	home, ok := SessionHome(sessionDir, eng, m)
	if !ok || eng.Home().TranscriptStoreRel == "" {
		return "", false
	}
	return filepath.Join(sessionDir, paths.NativeDirName, filepath.Base(home)), true
}

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
	HomeMode    agents.HomeMode
	// Auth is the mode the run authenticates in, settled by RunAuth; the
	// cells adapter resolves it to the run's credentials against the engine
	// it binds.
	Auth engine.AuthMode
	// EnvHost is the binding's env_host and env keys (agents.Agent.HostEnv): the host environment
	// stamps it on its Placement; a container has no inherited env to narrow.
	EnvHost agents.EnvHost
	// Env is the run's own environment: the identity carriers the cell's
	// session state is keyed from and the caller's passthrough.
	Env map[string]string
}

// Placement is a prepared environment's outcome, as the engine is handed it:
// every root on both sides, the env that makes each declared need true where
// the engine runs, and the declared home vars as resolved. The host and the
// container environment both return it, through the same calls, so nothing
// that reads it can tell which one prepared it.
type Placement struct {
	// Paths are the roots: ProjectRoot's Engine side is the engine's cwd.
	Paths present.Mapped
	// Env is what the environment sets for the engine: the home var, the
	// workspace's own provisions, the mode's credential set.
	Env map[string]string
	// Unset names variables the engine's process must NOT inherit from the
	// runner's own environment (engine.Credentials.Unset): the runner removes
	// them before it drives the engine. An empty value in Env is not the
	// same thing, and for some variables it means something else entirely.
	Unset []string
	// SecretFiles maps each credential variable whose VALUE must not cross
	// the coordinator link to the Engine-side path of the read-only file that
	// holds it; the runner reads each one and sets the variable for the
	// engine alone. Env never names a variable named here. A container cell
	// carries its credential this way, because its runner may dial home over
	// a LAN-visible cleartext listener (present.Listen.Public).
	SecretFiles map[string]string
	// EnvHost is which of the runner's own variables, of those Unset leaves,
	// the engine inherits (agents.EnvHost.Inherits): the binding's
	// declaration plus the engine's home var names, set only by the host
	// environment. Zero inherits every one.
	EnvHost agents.EnvHost
	// Home is each declared home var as resolved, at its Engine side.
	Home []engine.HomeBinding
	// Trust is the engine's verdict on the repository the workspace is in
	// (engine.Engine.Trust), taken where the session home's trust answer is
	// decided, so the answer and the launch flags cannot disagree.
	Trust engine.WorkspaceTrust
}

// Cell is a prepared place to run. A Cell exists only inside a Launch.
type Cell struct {
	// Placement is embedded so every Cell.Paths / .Env / .Home reader reads
	// the environment's outcome directly.
	Placement
	// HomeMode is the engine-home policy this cell was prepared under: the
	// session home, or the real one the binding selected — the unsafe
	// selection a plan and a banner name. Local to the launching process;
	// Home is what crosses the wire.
	HomeMode agents.HomeMode
	// Listen is what the coordinator must listen on so this cell's runner can
	// dial home: zero for a host cell and for a runtime that routes to the
	// host's loopback. Local to the launching process, like HomeMode: the
	// coordinator honours it before the runner starts.
	Listen present.Listen
	// Credential is where this cell's engine credential comes from (never
	// the credential): runs whose sources are equal share one principal, so
	// one refusal parks them together. Local to the launching process, like
	// HomeMode: the coordinator reads it, the runner never needs it.
	Credential engine.CredentialSource
	// CredentialFingerprint is the captured credential's one-way digest
	// (engine.Credentials.Fingerprint), "" when none was captured. Local to
	// the launching process, like Credential: a refused credential's hold
	// keeps it, so a restarted coordinator can tell a re-authenticated
	// environment from the one that was refused.
	CredentialFingerprint string
	// SecretsFile is the host path of the file the runner reads Placement's
	// SecretFiles from at every turn, "" when none exists before the runner
	// starts. Local to the launching process: a restarted coordinator
	// rewrites it with a freshly resolved credential for a run it re-adopts.
	SecretsFile string
	Cleanup     func() error
	// Handle is what the cells adapter keeps to START a process in this cell:
	// the prepared environment itself, opaque to core and read back only by
	// the adapter that made it.
	Handle any
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
	Permission engine.PermissionPolicy // resolved ONCE, here
	// Declared is the isolation request as it was ASKED — the invocation's
	// workspace and the binding's runtime, each empty where nothing declared
	// it — kept apart from Axes, the pair it settled to. A preview reports
	// both, so an axis nobody set is never shown as the guarantee its
	// default happens to be.
	Declared Axes
	Axes     Axes
	Cell     Cell
	Home     []engine.HomeBinding
	Trust    engine.WorkspaceTrust // the cell's verdict on the repository; the engine enforces it on every launch
	Package  composite.Carrier     // encoded then carried (inline or claim); both consumers redeem then Decode
	Exports  engine.Exports
	Plan     delivery.Plan
	Index    composite.Index
	MCP      sessions.Endpoint // minted per launch in Resolve, never persisted; the runner BINDS it
	Prompt   string
	Resume   sessions.ResumeRef
	Env      map[string]string // engine passthrough only
}

var (
	ErrNoIdentity           = errors.New("launch: a launch needs a minted identity")
	ErrModeUnsupported      = errors.New("launch: the engine does not declare the requested mode")
	ErrOwnershipMismatch    = errors.New("launch: the binding's container ownership is not available")
	ErrRuntimeUnavailable   = errors.New("launch: the requested runtime is not available")
	ErrNoAgent              = errors.New("launch: the named agent is not found")
	ErrNoEngine             = errors.New("launch: the label names no composed engine")
	ErrPermissionUnhonoured = errors.New("launch: the declared permission posture cannot be honoured")
	// ErrPlansFirstNeedsHuman is a posture that plans first under an
	// approver that is not the human: nobody could ever approve its plan.
	// It is always wrapped in ErrPermissionUnhonoured.
	ErrPlansFirstNeedsHuman = errors.New("launch: a posture that plans first needs the human as its approver")
	// ErrPlansFirstNoApprovalRoute is a structured run that plans first
	// with no session endpoint: its plan's approval has no route to travel.
	ErrPlansFirstNoApprovalRoute = errors.New("launch: a structured run that plans first needs a session endpoint to carry its plan's approval")
	ErrContextEmpty              = errors.New("launch: the named profile set assembled to nothing")
	ErrNoClaimCheck              = errors.New("launch: the package exceeds the inline ceiling and no claim check is composed")
	ErrBindingRoots              = errors.New("launch: the binding's root selection does not parse")
)

// Open is the in-process consumer of the carrier — the local launcher's
// half of the codec the runner shares: redeem by the carrier's shape with
// the same two transports Resolve carried with, then decode.
func Open(ctx context.Context, deps Deps, l Launch) (composite.Package, error) {
	return composite.Open(ctx, deps.Inline, deps.ClaimCheck, l.Package)
}

// WithLead is the resolved launch with the caller's context blocks appended
// to its package (composite.Package.WithLead) and the package carried again
// — for a block the caller can only compose AFTER Resolve, such as the
// startup findings the cell's preparation raised. A launch with nothing to
// add is returned as it is.
func WithLead(ctx context.Context, deps Deps, l Launch, blocks ...composite.Fragment) (Launch, error) {
	if len(blocks) == 0 {
		return l, nil
	}
	pkg, err := Open(ctx, deps, l)
	if err != nil {
		return Launch{}, err
	}
	enc, err := composite.Encode(pkg.WithLead(blocks...))
	if err != nil {
		return Launch{}, err
	}
	carrier, err := carry(ctx, deps, enc)
	if err != nil {
		return Launch{}, err
	}
	l.Package = carrier
	return l, nil
}

// Loadout is what a delivery of this launch consumes, over the decoded
// package: the ONE builder, so the runner and the local launcher deliver
// the same value.
func (l Launch) Loadout(pkg composite.Package) delivery.Loadout {
	roots := l.Cell.Paths.Paths()
	return delivery.Loadout{Plan: l.Plan, Package: pkg, Exports: l.Exports, Index: l.Index, MCP: l.MCP, Identity: l.Identity, WorkDir: roots.ProjectRoot.Host, SessionHome: roots.SessionHome.Host}
}

// Target is where this launch's static items land: the cell's advised
// roots, under the session's own writer tag, recorded in records.
func (l Launch) Target(records delivery.Ownership) delivery.Target {
	return delivery.Target{Root: present.New(l.Cell.Paths), Ownership: records, Writer: delivery.SessionWriter(l.Identity.Harp)}
}

// EngineEnv is the environment the engine process is started with: the
// cell's own env, the caller's passthrough over it, and the identity
// carriers the hooks read (sessions.HookEnv) over both, with the
// session-owner marker decided last (markOwner) so nothing beneath it can
// forge or keep it. Every arm that starts the engine reads it here, so no arm
// merges its own.
func (l Launch) EngineEnv() map[string]string {
	env := map[string]string{}
	maps.Copy(env, l.Cell.Env)
	maps.Copy(env, l.Env)
	maps.Copy(env, sessions.HookEnv(l.Identity))
	markOwner(env, l.Identity, l.Mode)
	return env
}

// Session is the ONLY constructor of the engine-facing projection.
func (l Launch) Session() engine.Session {
	return engine.Session{
		Identity: l.Identity, Label: l.Label, Mode: l.Mode, Permission: l.Permission,
		Roots: l.Cell.Paths.Paths(), WorkDir: l.Cell.Paths.Paths().ProjectRoot.Engine, Home: l.Home, MCP: l.MCP,
		Prompt: l.Prompt, Resume: l.Resume, Env: l.Env, Trust: l.Trust,
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
