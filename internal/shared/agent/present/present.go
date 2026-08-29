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
	"path"
	"path/filepath"
)

// Root is one directory named on two sides: Host is where its bytes live —
// what a writer opens and what the runtime bind-mounts FROM. Engine is what
// the engine is told — the path it opens, or a variable's value, or a mount
// target. The two axes that rewrite a Root are independent and touch
// different halves: a workspace advice (worktree materialization) rewrites
// Host, and a runtime advice (containerization) rewrites Engine and records a
// Mount. Uncontainerized, Engine equals Host.
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
	// EngineHome is the engine's own home (CODEX_HOME and the like).
	EngineHome Root
	// CtxloomHome is ctxloom's own: spool, sessions, task log.
	CtxloomHome Root
	// Scratch is this run's per-run materialization target.
	Scratch Root
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
		EngineHome:  identity(p.EngineHome),
		CtxloomHome: identity(p.CtxloomHome),
		Scratch:     identity(p.Scratch),
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
	// ProjectRoot, EngineHome, CtxloomHome and Scratch are the directories
	// each root becomes visible at inside the container. Empty means "mount
	// at the same path the host used."
	ProjectRoot, EngineHome, CtxloomHome, Scratch string
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
	return Paths{
		ProjectRoot: remap(p.ProjectRoot, c.ProjectRoot),
		EngineHome:  remap(p.EngineHome, c.EngineHome),
		CtxloomHome: remap(p.CtxloomHome, c.CtxloomHome),
		Scratch:     remap(p.Scratch, c.Scratch),
	}, mounts
}

// Apply runs the advice and bundles the result with the mounts it recorded.
func (c Containerize) Apply(p Paths) Mapped {
	paths, mounts := c.ApplyPaths(p)
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

// Presentation is the RESULT, built up by the chain. Never selected from a
// set.
type Presentation struct {
	// HostPath is where the fs layer wrote the bytes.
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

// UnderEngineHome roots the composition at Rel beneath the engine's home.
func (s Start) UnderEngineHome(rel string) Rooted { return under(s.mapped.paths.EngineHome, rel) }

// UnderCtxloomHome roots the composition at Rel beneath ctxloom's own home.
func (s Start) UnderCtxloomHome(rel string) Rooted { return under(s.mapped.paths.CtxloomHome, rel) }

// UnderScratch roots the composition at Rel beneath this run's scratch
// target.
func (s Start) UnderScratch(rel string) Rooted { return under(s.mapped.paths.Scratch, rel) }

// Served is terminal: an endpoint the engine connects to, not a file it
// reads. There is no root and no HostPath, because nothing here was
// materialized to disk — a served surface's bytes, if any, are owned by
// whatever is listening at the endpoint, not by this composition.
//
// endpoint is a Root like any other, which is what lets a served surface
// travel through the SAME advice as a file-backed one: Containerize rewrites
// endpoint.Engine (a container-reachable address, e.g. a mapped port or
// host.docker.internal) exactly as it rewrites a mounted directory's Engine
// side, with no served-specific case anywhere in the advice. envVar names it
// to the engine, the same way AnnounceEnv does for a rooted composition.
func (s Start) Served(endpoint Root, envVar string) Presentation {
	return Presentation{Env: map[string]string{envVar: endpoint.Engine}}
}

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
