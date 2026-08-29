// Package present composes how a delivered file is PRESENTED to an engine:
// where its bytes live on the host, where the engine sees them, and what the
// engine is told about them.
//
// Two things are distinguished, and conflating them is what lets a defect hide:
//
//	SURFACE   WHAT is delivered.
//	CHANNEL   HOW the engine reaches it — the path, argv, the environment,
//	          container mounts.
//
// One surface travels several channels at once: bytes at a path, that path
// named on argv, a home named in the environment. An ADVICE is one link that
// contributes to or transforms a channel, and THE IMPLEMENTING TYPE IS THE
// DECLARATION: an advice touches exactly the channels whose interfaces it
// implements, so there is no table pairing an advice with its channels and
// therefore nothing for such a table to drift from.
//
// A presentation is COMPOSED, never selected from a set, and the type a layer
// returns offers only the layers that may legally follow it. An illegal ORDER
// is therefore not writable at all, rather than accepted here and failed at run
// time.
//
// Build is TOTAL wherever it is offered: reaching a state that has Build is
// itself the proof that the composition is complete, so there is no error to
// return and no completeness check for a caller to forget.
package present

import (
	"path/filepath"
	"strings"
)

// Presentation is the RESULT, built up by the chain. Never selected from a set.
type Presentation struct {
	// HostPath is where the fs layer wrote the bytes.
	HostPath string
	// EnginePath is where the ENGINE sees them. Equal to HostPath until a
	// remapping layer changes it.
	EnginePath string
	// Args are the argv channel.
	Args []string
	// Env is the environment channel.
	Env map[string]string
	// Mounts are the mount channel.
	Mounts []Mount
}

// Mount makes HostDir visible to the engine at TargetDir.
type Mount struct{ HostDir, TargetDir string }

// --- channels ---------------------------------------------------------------
//
// Every write to a Presentation goes through one of these, so a channel an
// advice touches without declaring is not something to remember: it does not
// compile.

// PathChannel is the channel of WHERE THE BYTES ARE — the host path and the
// path the engine reads.
type PathChannel interface {
	ApplyPath(Presentation) Presentation
}

// EnvChannel is the channel of WHAT THE ENGINE IS TOLD IN ITS ENVIRONMENT.
//
// Implementing it is also what makes a composition's location a LEVER: an
// advice that names the location through a variable can be told a different
// one. Containerization asks nothing of the engine and consults no table — it
// type-asserts this interface over the advice that rooted the composition, and
// the answer is the declaration itself.
type EnvChannel interface {
	ApplyEnv(Presentation) Presentation
}

// ArgvChannel is the channel of WHAT THE ENGINE IS TOLD ON ARGV.
type ArgvChannel interface {
	ApplyArgv(Presentation) Presentation
}

// MountChannel is the channel of WHAT A CONTAINER MAKES VISIBLE.
type MountChannel interface {
	ApplyMounts(Presentation) Presentation
}

// The declarations: each variant states, in the type system, which channels it
// contributes to.
//
// These record the model; they do not defend it. Every method named here also
// has a direct call site, so a variant that dropped one would fail to build
// there first and these lines would never be reached. What genuinely needs
// defending is the LEVER — that EnvDirFile satisfies EnvChannel and
// ProjectRootFile does not — because containerization discovers it by type
// assertion, where a missing method is a silent false, not a compile error.
// Go cannot assert a negative conformance, so that pair is pinned by
// TestTheLeverIsDECLAREDByTheInterface instead.
var (
	_ PathChannel = ProjectRootFile{}
	_ PathChannel = EnvDirFile{}
	_ EnvChannel  = EnvDirFile{}
	_ ArgvChannel = FlagValue{}

	_ PathChannel  = ContainerMount{}
	_ EnvChannel   = ContainerMount{}
	_ MountChannel = ContainerMount{}
)

// --- variants: data that CARRIES its behaviour -----------------------------

// ProjectRootFile writes at Rel under the resolved project root.
//
// It touches the path channel and nothing else, and that absence is the whole
// statement: the engine finds this file by its own fixed rule, so there is no
// variable and no flag through which it could be told a different location.
type ProjectRootFile struct {
	Rel string

	// root is filled in by the layer that resolves it.
	root string
}

// ApplyPath puts the bytes at Rel under the resolved project root.
func (v ProjectRootFile) ApplyPath(p Presentation) Presentation {
	p.HostPath = filepath.Join(v.root, v.Rel)
	p.EnginePath = p.HostPath
	return p
}

// EnvDirFile resolves EnvVar (or HomeDefault under $HOME) to a root and writes
// at Rel beneath it — the relocated-home mechanism.
//
// It touches the path AND environment channels: the bytes sit beneath a home,
// and the home is named to the engine through a variable. Naming it is what
// makes it movable.
type EnvDirFile struct {
	EnvVar, HomeDefault, Rel string

	// root is filled in by the layer that resolves it.
	root string
}

// ApplyPath puts the bytes at Rel beneath the resolved home.
func (v EnvDirFile) ApplyPath(p Presentation) Presentation {
	p.HostPath = filepath.Join(v.root, v.Rel)
	p.EnginePath = p.HostPath
	return p
}

// ApplyEnv names the home to the engine.
func (v EnvDirFile) ApplyEnv(p Presentation) Presentation {
	env := copyEnv(p.Env)
	env[v.EnvVar] = v.root
	p.Env = env
	return p
}

// FlagValue names the resolved path on argv.
type FlagValue struct{ Flag string }

// ApplyArgv names the ENGINE path, never the host one.
func (v FlagValue) ApplyArgv(p Presentation) Presentation {
	p.Args = append(p.Args, v.Flag, p.EnginePath)
	return p
}

// ContainerMount maps a composition's root into the engine's namespace.
//
// It touches the path, environment and mount channels: the engine path moves,
// every environment value naming the root moves with it, and the root is made
// visible inside the container. The invariant across all three is that AFTER
// THIS ADVICE, NOTHING THE ENGINE READS STILL NAMES A HOST PATH.
//
// Leaving the environment behind is the worse half to get wrong. Argv naming an
// unmounted path fails visibly; a home variable naming a host path absent from
// the container makes the engine look somewhere empty and behave as though it
// was never configured — a success with nothing in it.
type ContainerMount struct {
	// TargetDir is where the root becomes visible to the engine. It is
	// honoured only where the composition HAS a lever; where the engine fixed
	// the location, the root is mounted at itself and this is ignored.
	TargetDir string

	// root is filled in from the composition being mapped.
	root string
}

// ApplyPath moves the engine path under the target.
func (v ContainerMount) ApplyPath(p Presentation) Presentation {
	p.EnginePath = remapUnder(p.HostPath, v.root, v.TargetDir)
	return p
}

// ApplyEnv moves EVERY value naming the root, not merely the one an advice
// happens to have written. The invariant is a statement about everything the
// engine reads, so a second entry beneath the home is covered by the same rule
// as the home itself.
func (v ContainerMount) ApplyEnv(p Presentation) Presentation {
	if len(p.Env) == 0 {
		return p
	}
	env := make(map[string]string, len(p.Env))
	for k, val := range p.Env {
		env[k] = remapUnder(val, v.root, v.TargetDir)
	}
	p.Env = env
	return p
}

// ApplyMounts makes the root visible at the target.
func (v ContainerMount) ApplyMounts(p Presentation) Presentation {
	p.Mounts = append(p.Mounts, Mount{HostDir: v.root, TargetDir: v.TargetDir})
	return p
}

// --- the typestate chain ---------------------------------------------------

// Start accepts a root-producing variant and nothing else: no layer may name a
// path before one exists.
type Start struct{ projectRoot string }

// New begins a composition against a resolved project root.
func New(projectRoot string) Start { return Start{projectRoot: projectRoot} }

// WithProjectRootFile materializes under the project root.
func (s Start) WithProjectRootFile(v ProjectRootFile) Rooted {
	v.root = s.projectRoot
	return Rooted{p: v.ApplyPath(Presentation{}), by: v}
}

// WithEnvDirFile materializes under a relocated home.
func (s Start) WithEnvDirFile(v EnvDirFile, resolvedRoot string) Rooted {
	v.root = resolvedRoot
	return Rooted{p: v.ApplyEnv(v.ApplyPath(Presentation{})), by: v}
}

// Rooted has bytes at a host path. Containerization may remap it, an engine may
// name it, or it may stand alone at a conventional path.
//
// WithProjectRootFile is ABSENT here: re-rooting a composition that already has
// a root is not a thing, so it cannot be written.
type Rooted struct {
	p Presentation
	// by is the advice that rooted this composition, kept so a later advice can
	// read the channels it DECLARES. It is the declaration, not a second copy
	// of the values: everything it wrote is already in p, and p is what a later
	// advice transforms.
	by PathChannel
}

// WithContainerMount maps the composition's root into the engine's namespace.
//
// The target is a REQUEST, and whether it is honoured is discovered from the
// rooting advice rather than asked of the engine: an advice on the environment
// channel names its location through a variable, so the container may put the
// root anywhere and tell it. An advice with no such channel has no lever, the
// engine will look at the path it always looks at, and the only mount that
// leaves it reachable is one AT that same path.
//
// It transforms what it RECEIVES. It does not recompute from whatever the
// composition was first built from, so an advice that already moved the root is
// carried through rather than discarded.
func (r Rooted) WithContainerMount(v ContainerMount) Mapped {
	v.root = hostRoot(r.p)
	if _, lever := r.by.(EnvChannel); !lever {
		v.TargetDir = v.root
	}
	p := v.ApplyPath(r.p)
	p = v.ApplyEnv(p)
	p = v.ApplyMounts(p)
	return Mapped{p: p}
}

// WithFlag names the ENGINE path on argv.
func (r Rooted) WithFlag(v FlagValue) Flagged { return Flagged{p: v.ApplyArgv(r.p)} }

// Build completes a conventional-path composition — the engine finds the file
// by its own rule, so nothing needs to name it.
func (r Rooted) Build() Presentation { return r.p }

// Mapped has been remapped into the engine's namespace. WithContainerMount is
// ABSENT: a composition is mapped once.
type Mapped struct{ p Presentation }

// WithFlag names the remapped ENGINE path, never the host one.
func (m Mapped) WithFlag(v FlagValue) Flagged { return Flagged{p: v.ApplyArgv(m.p)} }

// Build completes a mapped composition with no flag.
func (m Mapped) Build() Presentation { return m.p }

// Flagged is terminal: the path has been named. Neither rooting nor mapping can
// follow, because argv already quotes the path they would change.
type Flagged struct{ p Presentation }

// Build returns the composition. TOTAL — reaching this state is the proof.
func (f Flagged) Build() Presentation { return f.p }

// hostRoot is the host directory a composition is rooted at, derived FROM THE
// COMPOSITION: the home an environment value names, when the bytes sit beneath
// one, and otherwise the file's own directory.
//
// Reading the home out of the environment channel is what keeps a later advice
// a link in a chain rather than a second beginning. That value is the live
// authority on where the home is, so whatever set it last is what gets mapped;
// deriving the root from anything recorded earlier would discard the advice
// that set it and map a directory nothing points at.
//
// The OUTERMOST match wins. A nearer ancestor is a subdirectory of the home,
// and mounting that would leave the home the engine is pointed at absent from
// the container.
func hostRoot(p Presentation) string {
	root := filepath.Dir(p.HostPath)
	for _, v := range p.Env {
		if isUnder(p.HostPath, v) && len(v) < len(root) {
			root = v
		}
	}
	return root
}

// isUnder reports whether path is AT or BENEATH root.
func isUnder(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// remapUnder rewrites a host path at or beneath root to sit under target, and
// leaves anything else alone.
func remapUnder(path, root, target string) string {
	if !isUnder(path, root) {
		return path
	}
	if path == root {
		return target
	}
	return filepath.Join(target, strings.TrimPrefix(path, root+string(filepath.Separator)))
}

// copyEnv returns a map safe to write: the caller still holds the composition
// this was built from, and composing must not reach back and alter it.
func copyEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+1)
	for k, v := range env {
		out[k] = v
	}
	return out
}
