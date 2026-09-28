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
// The path rewrite happens BEFORE any presenter runs, as PRE-ADVICE over a
// Paths object: two independent axes (workspace, runtime) each rewrite one
// half of every root — Host or Engine — and the runtime axis alone emits
// Mounts. A presenter then composes against the ALREADY-rewritten roots and
// never touches containerization at all, so a presenter that only names a
// flag on argv is exactly as containerizable as one that also names an
// environment variable: neither one performs the rewrite, so neither one
// needs a lever to do it with.
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
// opens, or a variable's value, or a mount target. The two axes that rewrite
// a Root are independent and touch different halves: a workspace advice
// (worktree materialization) rewrites Host, and a runtime advice
// (containerization) rewrites Engine and records a Mount. Uncontainerized,
// Engine equals Host; for a writer that shares the engine's filesystem, see
// Mapped.EngineSide.
type Root struct{ Host, Engine string }

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

// Mount makes HostDir visible to the engine at TargetDir.
type Mount struct{ HostDir, TargetDir string }

// PathsAdvice rewrites every root in a Paths and reports what a container
// runtime must mount to make the rewrite true. It is applied EXACTLY ONCE,
// to the whole Paths, before any presenter runs — never per-surface, and
// never asked of a presenter or an engine.
type PathsAdvice interface {
	ApplyPaths(Paths) (Paths, []Mount)
}

// Host is the identity advice: Engine equals Host on every root, and nothing
// is mounted. It is the host transport — not a special case a call site
// branches to, but the same PathsAdvice vocabulary as Containerize, so a run
// that never containerizes still composes through the identical pipeline.
type Host struct{}

var _ PathsAdvice = Host{}

// ApplyPaths implements PathsAdvice with the identity rewrite.
func (Host) ApplyPaths(p Paths) (Paths, []Mount) {
	return Paths{
		ProjectRoot: identity(p.ProjectRoot),
		SessionHome: identity(p.SessionHome),
	}, nil
}

func identity(r Root) Root {
	return Root{Host: r.Host, Engine: r.Host}
}

// OnHost applies the identity advice directly — for a call site that already
// knows it is uncontainerized and has no PathsAdvice value of its own to
// hold. It exists so that call site never has to special-case "not
// containerized": it calls OnHost exactly where a containerized run would
// call Containerize{...}.Apply, and gets the same Mapped shape back.
func OnHost(p Paths) Mapped {
	paths, mounts := Host{}.ApplyPaths(p)
	return Mapped{paths: paths, mounts: mounts}
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

// Containerize is the runtime advice: it rewrites Engine on every root that
// has a target and mounts the root to make that true.
//
// A root with no configured target is mounted AT ITS OWN HOST PATH — the
// engine fixed that location and has no variable through which it could be
// told a different one, so the only mount that leaves it reachable is one at
// the same path it always looks at. This was previously discovered per
// composition by asking whether the rooting advice implemented a channel
// interface; here it is a static decision made once, for the whole run,
// by whoever configures Containerize — containerization never interrogates
// a presenter or an engine to make it.
//
// A zero-value root (Host == "") is left alone and contributes no mount: it
// names a root this run never resolved, so there is nothing to mount.
type Containerize struct {
	// ProjectRoot and SessionHome are the directories each root becomes
	// visible at inside the container. Empty means "mount at the same path
	// the host used."
	ProjectRoot, SessionHome string
}

var _ PathsAdvice = Containerize{}

// ApplyPaths implements PathsAdvice.
func (c Containerize) ApplyPaths(p Paths) (Paths, []Mount) {
	var mounts []Mount
	remap := func(r Root, target string) Root {
		if r.Host == "" {
			return r
		}
		if target == "" {
			target = r.Host
		}
		mounts = append(mounts, Mount{HostDir: r.Host, TargetDir: target})
		return Root{Host: r.Host, Engine: target}
	}
	// remap APPENDS to mounts, and mounts is also a result operand. Go
	// specifies left-to-right order only among the CALLS in a statement's
	// operands; when a plain variable operand is read relative to those calls
	// is unspecified, so `return Paths{remap(...), ...}, mounts` may return
	// the pre-append mounts. Settling the composite literal in its own
	// statement first makes the appends complete before mounts is read.
	out := Paths{
		ProjectRoot: remap(p.ProjectRoot, c.ProjectRoot),
		SessionHome: remap(p.SessionHome, c.SessionHome),
	}
	return out, mounts
}

// Apply runs the advice and bundles the result with the mounts it recorded.
func (c Containerize) Apply(p Paths) Mapped {
	paths, mounts := c.ApplyPaths(p)
	return Mapped{paths: paths, mounts: mounts}
}

// Advised rebuilds a Mapped from an advised Paths and the mounts that made
// it true. It is the wire codec's constructor and nobody else's: the advice
// was applied exactly once on the originator, and the runner receives its
// RESULT — both sides of every root and the mount list — rather than
// applying any advice of its own.
func Advised(paths Paths, mounts []Mount) Mapped {
	return Mapped{paths: paths, mounts: mounts}
}

// Mapped is a Paths that has been advised: every root's Engine side is
// settled, and every mount a container runtime must honour to make that true
// has been recorded. It is the ONLY thing New accepts, so a raw Paths cannot
// reach a presenter — the rewrite is not optional and not repeatable per
// surface.
type Mapped struct {
	paths  Paths
	mounts []Mount
}

// Paths returns the advised roots.
func (m Mapped) Paths() Paths { return m.paths }

// Mounts returns every mount the advice that produced this Mapped recorded.
// It is a property of the WHOLE RUN, not of any one presentation: a runtime
// reads it once, when it launches, rather than once per surface.
func (m Mapped) Mounts() []Mount { return m.mounts }

// EngineSide is this Mapped as seen by a writer that shares the ENGINE's
// filesystem — the runner that is a container's foreground process: every
// root's Host becomes its Engine side, because the host side of a relocated
// root is not mounted where that writer runs. Mounts are kept as the run's
// record; nothing beside the engine binds them.
func (m Mapped) EngineSide() Mapped {
	side := func(r Root) Root { return Root{Host: r.Engine, Engine: r.Engine} }
	p := m.paths
	return Mapped{
		paths: Paths{
			ProjectRoot: side(p.ProjectRoot),
			SessionHome: side(p.SessionHome),
		},
		mounts: m.mounts,
	}
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

// under materializes a Rooted from a Root and a relative path.
func under(root Root, rel string) Rooted {
	return Rooted{
		p:    Presentation{HostPath: root.Host, EnginePath: root.Engine}.Beneath(rel),
		root: root,
	}
}

// Beneath extends p by rel, a slash-separated relative path. HostPath is
// OS-native, because a writer opens it on THIS host. Where the engine sees
// the host's own filesystem (EnginePath equals HostPath) the engine path IS
// the host path, separators included — a Windows host's local engine opens
// C:\... like everyone else there. Once they differ, EnginePath names a
// container path, and a container is Linux regardless of the host, so it is
// joined with '/': filepath.Join would carry the host's separator into a path
// the engine can never open.
func (p Presentation) Beneath(rel string) Presentation {
	host := filepath.Join(p.HostPath, filepath.FromSlash(rel))
	if p.EnginePath == p.HostPath {
		return Presentation{HostPath: host, EnginePath: host}
	}
	return Presentation{HostPath: host, EnginePath: path.Join(p.EnginePath, rel)}
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
