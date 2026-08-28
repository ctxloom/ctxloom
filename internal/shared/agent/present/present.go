// Package present composes how a delivered file is PRESENTED to an engine:
// where its bytes live on the host, where the engine sees them, and what the
// engine is told about them.
//
// A presentation is COMPOSED, never selected from a set. Each layer contributes
// one role — rooting, remapping, naming — and the type a layer returns offers
// only the layers that may legally follow it. An illegal ORDER is therefore not
// writable at all, rather than accepted here and failed at run time.
//
// Build is TOTAL wherever it is offered: reaching a state that has Build is
// itself the proof that the composition is complete, so there is no error to
// return and no completeness check for a caller to forget.
package present

import "path/filepath"

// Presentation is the RESULT, built up by the chain. Never selected from a set.
type Presentation struct {
	// HostPath is where the fs layer wrote the bytes.
	HostPath string
	// EnginePath is where the ENGINE sees them. Equal to HostPath until a
	// remapping layer changes it.
	EnginePath string
	// Args are argv contributed by the engine layer.
	Args []string
	// Env is contributed by a relocated-home layer.
	Env map[string]string
	// Mounts are contributed by containerization.
	Mounts []Mount
}

// Mount makes HostDir visible to the engine at TargetDir.
type Mount struct{ HostDir, TargetDir string }

// --- variants: data that CARRIES its behaviour -----------------------------

// ProjectRootFile writes at Rel under the resolved project root.
type ProjectRootFile struct{ Rel string }

// EnvDirFile resolves EnvVar (or HomeDefault under $HOME) to a root and writes
// at Rel beneath it — the relocated-home mechanism.
type EnvDirFile struct{ EnvVar, HomeDefault, Rel string }

// FlagValue names the resolved path on argv.
type FlagValue struct{ Flag string }

// --- the typestate chain ---------------------------------------------------

// Start accepts a root-producing variant and nothing else: no layer may name a
// path before one exists.
type Start struct{ projectRoot string }

// New begins a composition against a resolved project root.
func New(projectRoot string) Start { return Start{projectRoot: projectRoot} }

// WithProjectRootFile materializes under the project root.
func (s Start) WithProjectRootFile(v ProjectRootFile) Rooted {
	p := Presentation{HostPath: filepath.Join(s.projectRoot, v.Rel)}
	p.EnginePath = p.HostPath
	return Rooted{p: p}
}

// WithEnvDirFile materializes under a relocated home.
func (s Start) WithEnvDirFile(v EnvDirFile, resolvedRoot string) Rooted {
	p := Presentation{
		HostPath: filepath.Join(resolvedRoot, v.Rel),
		Env:      map[string]string{v.EnvVar: resolvedRoot},
	}
	p.EnginePath = p.HostPath
	return Rooted{p: p}
}

// Rooted has bytes at a host path. Containerization may remap it, an engine may
// name it, or it may stand alone at a conventional path.
//
// WithProjectRootFile is ABSENT here: re-rooting a composition that already has
// a root is not a thing, so it cannot be written.
type Rooted struct{ p Presentation }

// WithContainerMount maps the host path into the engine's namespace.
func (r Rooted) WithContainerMount(targetDir string) Mapped {
	p := r.p
	p.Mounts = append(p.Mounts, Mount{HostDir: filepath.Dir(p.HostPath), TargetDir: targetDir})
	p.EnginePath = filepath.Join(targetDir, filepath.Base(p.HostPath))
	return Mapped{p: p}
}

// WithFlag names the ENGINE path on argv.
func (r Rooted) WithFlag(v FlagValue) Flagged { return Flagged{p: withFlag(r.p, v)} }

// Build completes a conventional-path composition — the engine finds the file
// by its own rule, so nothing needs to name it.
func (r Rooted) Build() Presentation { return r.p }

// Mapped has been remapped into the engine's namespace. WithContainerMount is
// ABSENT: a composition is mapped once.
type Mapped struct{ p Presentation }

// WithFlag names the remapped ENGINE path, never the host one.
func (m Mapped) WithFlag(v FlagValue) Flagged { return Flagged{p: withFlag(m.p, v)} }

// Build completes a mapped composition with no flag.
func (m Mapped) Build() Presentation { return m.p }

// Flagged is terminal: the path has been named. Neither rooting nor mapping can
// follow, because argv already quotes the path they would change.
type Flagged struct{ p Presentation }

// Build returns the composition. TOTAL — reaching this state is the proof.
func (f Flagged) Build() Presentation { return f.p }

// withFlag appends the flag naming the ENGINE path. Shared by Rooted and
// Mapped so the two cannot disagree about which path argv quotes.
func withFlag(p Presentation, v FlagValue) Presentation {
	p.Args = append(p.Args, v.Flag, p.EnginePath)
	return p
}
