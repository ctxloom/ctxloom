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
// The path rewrite happens BEFORE any presenter runs: the run's environment
// (isolation) relocates every root once, producing a Mapped whose Host and
// Engine sides are both settled, together with whatever mounts make the
// Engine side true — mounts this package never sees. A presenter then
// composes against the ALREADY-relocated roots and never touches
// containerization at all, so a presenter that only names a flag on argv is
// exactly as containerizable as one that also names an environment variable:
// neither one performs the rewrite, so neither one needs a lever to do it
// with.
//
// A presentation is COMPOSED, never selected from a set, and the type a layer
// returns offers only the layers that may legally follow it. An illegal ORDER
// is therefore not writable at all, rather than accepted here and failed at
// run time.
//
// Build is TOTAL wherever it is offered: reaching a state that has Build is
// itself the proof that the composition is complete, so there is no error to
// return and no completeness check for a caller to forget.
package present

import (
	"errors"
	"fmt"
	"net"
	neturl "net/url"
	"path"
	"path/filepath"
	"strings"
)

// Root is one directory named on two sides: Host is where its bytes live as
// the WRITER sees them — what a writer opens and, on the originator, what the
// runtime bind-mounts FROM. Engine is what the engine is told — the path it
// opens, or a variable's value, or a mount target. Where the engine runs in
// place, Engine equals Host; for a writer that shares the engine's
// filesystem, see Mapped.EngineSide.
type Root struct{ Host, Engine string }

// ErrUnreachableRoot is a root the environment cannot present to the engine:
// its host side has no route into the engine's filesystem. Only a relocating
// environment can raise it — one that presents a root in place never maps
// anything — and it is wrapped with the root it names.
var ErrUnreachableRoot = errors.New("present: a root has no route into the engine's filesystem")

// Paths is every root a presentation may build from, resolved once per run,
// before any presenter composes anything.
//
// Only roots something actually builds from belong here — a further root can
// be added later without changing Presenter's signature, or any engine's
// declaration, because a presenter reads a Paths field by name rather than
// receiving a positional argument.
type Paths struct {
	// ProjectRoot is the user's code.
	ProjectRoot Root
	// SessionHome is this session's home for the engine: the directory its
	// relocated config home (CLAUDE_CONFIG_DIR and the like) names, or, for
	// an engine that relocates nothing, the session-private root it is
	// delivered into. launch.SessionHome is the one rule that places it.
	SessionHome Root
}

// OnHost presents p in place — Engine equals Host on every root — for a
// caller at rest or probing the host (ProjectOnHost, an agent probe, the
// conformance suite, an engine's own forms). No launch path calls it: a
// launch's roots are an Environment's Placement.
func OnHost(p Paths) Mapped {
	inPlace := func(r Root) Root { return Root{Host: r.Host, Engine: r.Host} }
	return Mapped{paths: Paths{ProjectRoot: inPlace(p.ProjectRoot), SessionHome: inPlace(p.SessionHome)}}
}

// ProjectOnHost advises a run that has exactly ONE root — the project
// directory — and runs on the host. It is the entry for the at-rest callers
// (materialize, apply, remove) and for anything that writes into a directory
// the engine will read at the same path: they hold a dir and nothing else,
// so this is the whole of what they can truthfully advise. Every other root
// stays unresolved, and a composition that reaches for one gets the zero Root
// rather than a fabricated path.
func ProjectOnHost(dir string) Start {
	return New(OnHost(Paths{ProjectRoot: Root{Host: dir}}))
}

// Advised is a Mapped from roots whose both sides are already settled: an
// environment's relocation (isolation, the only place a path is rewritten),
// the wire codec rebuilding what the originator sent, and a composition that
// re-roots an already-advised Mapped. It rewrites nothing itself.
func Advised(paths Paths) Mapped {
	return Mapped{paths: paths}
}

// Mapped is a Paths whose every root has both sides settled. It is the ONLY
// thing New accepts, so a raw Paths cannot reach a presenter — the rewrite
// is not optional and not repeatable per surface.
type Mapped struct {
	paths Paths
}

// Paths returns the advised roots.
func (m Mapped) Paths() Paths { return m.paths }

// EngineSide is this Mapped as seen by a writer that shares the ENGINE's
// filesystem — the runner, wherever it runs: every root's Host becomes its
// Engine side, because the host side of a relocated root is not mounted
// where that writer runs. On the host the two sides are equal, so it is the
// identity there.
func (m Mapped) EngineSide() Mapped {
	side := func(r Root) Root { return Root{Host: r.Engine, Engine: r.Engine} }
	p := m.paths
	return Mapped{paths: Paths{ProjectRoot: side(p.ProjectRoot), SessionHome: side(p.SessionHome)}}
}

// Presentation is the RESULT, built up by the chain. Never selected from a
// set.
type Presentation struct {
	// HostPath is where the fs layer wrote the bytes, as the WRITING process
	// opens it on its own filesystem. When that process shares the engine's
	// filesystem (runner.Execute over a container cell, via
	// Mapped.EngineSide) HostPath equals EnginePath; only a writer outside
	// the engine's filesystem ever sees them differ.
	HostPath string
	// EnginePath is where the ENGINE sees them. Equal to HostPath wherever
	// Engine equals Host on the root this composition is rooted under.
	EnginePath string
	// Args are the argv channel.
	Args []string
	// Env is the environment channel.
	Env map[string]string
}

// --- the typestate chain -----------------------------------------------

// Start holds an already-advised Paths. No layer before it may name a path,
// because Start is the earliest state there is: New's parameter type is
// Mapped, not Paths, so a composition against un-rewritten roots does not
// compile — there is no method that would accept one.
type Start struct{ mapped Mapped }

// New begins a composition against an advised Paths.
func New(m Mapped) Start { return Start{mapped: m} }

// Paths returns the advised roots this composition builds from.
func (s Start) Paths() Paths { return s.mapped.Paths() }

// UnderProjectRoot roots the composition at Rel beneath the project root.
func (s Start) UnderProjectRoot(rel string) Rooted { return under(s.mapped.paths.ProjectRoot, rel) }

// UnderSessionHome roots the composition at Rel beneath the session home.
func (s Start) UnderSessionHome(rel string) Rooted { return under(s.mapped.paths.SessionHome, rel) }

// under materializes a Rooted from a Root and a relative path. HostPath is
// OS-native, because a writer opens it on THIS host. EnginePath is always
// joined with forward slashes: once Engine genuinely differs from Host it
// names a container path, and a container is Linux regardless of the host
// this process runs on — filepath.Join would carry the host's separator
// into a path the engine can never open. Where Engine equals Host (the
// uncontainerized case) this produces the identical string on every
// platform this project supports, since Host itself already uses '/'.
func under(root Root, rel string) Rooted {
	return Rooted{
		p: Presentation{
			HostPath:   filepath.Join(root.Host, rel),
			EnginePath: path.Join(root.Engine, rel),
		},
		root: root,
	}
}

// Rooted has bytes at a host path and knows the root they sit beneath.
// AnnounceEnv and AnnounceFlag return Rooted so they compose in any order
// and repeat — there is no mount left for them to invalidate, because
// mounting already happened before this composition began.
type Rooted struct {
	p Presentation
	// root is the directory this composition is rooted under — what
	// AnnounceEnv names, as opposed to p.EnginePath, the one file within it
	// that AnnounceFlag names.
	root Root
}

// AnnounceEnv names the ROOT — the directory, not the file — to the engine
// through an environment variable.
func (r Rooted) AnnounceEnv(v string) Rooted {
	env := make(map[string]string, len(r.p.Env)+1)
	for k, val := range r.p.Env {
		env[k] = val
	}
	env[v] = r.root.Engine
	r.p.Env = env
	return r
}

// AnnounceFlag names the FILE — the ENGINE path, never the host one — on
// argv.
func (r Rooted) AnnounceFlag(flag string) Rooted {
	args := make([]string, len(r.p.Args), len(r.p.Args)+2)
	copy(args, r.p.Args)
	r.p.Args = append(args, flag, r.p.EnginePath)
	return r
}

// Build completes the composition. TOTAL — reaching Rooted is itself the
// proof that a root exists; nothing further is required.
func (r Rooted) Build() Presentation { return r.p }

// Under reports whether path lies under root: equal to it, or root followed
// by a path separator. A prefix that merely shares characters with the root
// ("/home/x" under "/home/xy") is not under it.
func Under(path, root string) bool {
	if root == "" {
		return false
	}
	root = strings.TrimSuffix(root, "/")
	return path == root || strings.HasPrefix(path, root+"/")
}

// Reach is a coordinator endpoint in both presentations, as Root is a path:
// Host is the URL the coordinator minted for a runner beside it (its loopback
// listener), Engine the URL a runner inside the cell dials. Uncontainerized,
// Engine equals Host. A containerization re-mints Engine and names the
// Listen the coordinator must honour for Engine to land — as a Root's rewrite
// comes with its Mount.
type Reach struct{ Host, Engine string }

// Listen is the listener a re-minted Reach needs beyond Host's own. The zero
// value needs none: the runtime delivers Engine to Host's listener (a
// translator's route to the host's loopback).
type Listen struct {
	// Addr is the host address to listen on, on Host's port.
	Addr string
	// Public marks a listener reachable beyond this host (the fallback when a
	// runtime offers no private route); Why names that reason for the warning.
	Public bool
	Why    string
}

// ReachOnHost is the uncontainerized Reach: Engine is Host.
func ReachOnHost(url string) Reach { return Reach{Host: url, Engine: url} }

// Via re-mints r for a runner that reaches the host at dial: Engine is Host
// with its host replaced by dial, port and path kept — the listener that
// answers there shares Host's port.
func (r Reach) Via(dial string) (Reach, error) {
	u, err := neturl.Parse(r.Host)
	if err != nil {
		return Reach{}, fmt.Errorf("present: reach %q: %w", r.Host, err)
	}
	if u.Port() == "" {
		return Reach{}, fmt.Errorf("present: reach %q names no port", r.Host)
	}
	u.Host = net.JoinHostPort(dial, u.Port())
	return Reach{Host: r.Host, Engine: u.String()}, nil
}
