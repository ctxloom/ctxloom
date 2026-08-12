package kiro

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// backendName is kiro's REGISTERED backend name (internal/lm/backends), the key
// credentialSeedSpecs and the backends registry are both spelled with. It
// scopes this engine's project-scoped home so no two engines share one
// directory.
const backendName = "kiro"

// StateHome is the project-scoped config home ctxloom points kiro at for
// workDir — <workDir>/.ctxloom/state/engines/kiro — from which InTreeHome
// derives the actual KIRO_HOME.
//
// It is the SINGLE owner of that location: the run path (the in-tree agent-home
// contribution in internal/operations) and any future home-keyed static writer
// resolve through it, so a `run` and a `materialize` can never target different
// roots.
//
// WHY NOT the real ~/.kiro, which an in-tree kiro run used before this existed:
// KIRO_HOME relocates kiro's WHOLE home (kiro-cli >= 2.3.0 — global agents,
// prompts, skills, steering, settings AND sessions), so an agent run pointed at
// the human's own home reads their global agents and steering and writes its
// own session state back into them. An agent is not the human.
//
// The rule is AGENT runs only: the human's own interactive session keeps the
// real ~/.kiro. See docs/architecture/engines/isolation.md's engine config
// homes section, and operations.InTreeAgentHomeEnv, which is the one place that
// condition is decided.
//
// NO CREDENTIALS are seeded here and none need to be: kiro's subscription auth
// lives in a global sqlite under XDGDataHomeEnv that KIRO_HOME does not
// relocate (see HomeEnv's doc, live-verified against kiro-cli 2.12.1), so a
// FRESH KIRO_HOME stays authenticated. This is why the in-tree arm relocates
// KIRO_HOME and deliberately leaves XDG_DATA_HOME alone: relocating the
// credential store with nothing to seed into it would strand the agent logged
// out, which is exactly what the worktree axis' GatedOnCreds machinery refuses
// to do.
//
// kiro's CWD-KEYED surfaces — .kiro/steering, .kiro/settings — are untouched by
// this and stay at the project root where kiro natively reads them.
func StateHome(workDir string) string {
	return paths.EngineStateHome(filepath.Join(workDir, paths.AppDirName), backendName)
}

// inTreeHomeLeaf is the directory INSIDE StateHome that KIRO_HOME names. It
// equals the leaf the WORKTREE axis uses (internal/lm/isolation's
// credentialSeedSpecs["kiro"] KIRO_HOME HomeVar Subdir) so kiro's home layout is
// the same shape on every axis. The two literals live in different packages
// because isolation cannot import this one (kiro -> internal/acp ->
// internal/lm/isolation is a real cycle);
// TestInTreeHome_MatchesTheIsolationHomeVarSubdir is the gate that keeps them
// equal.
const inTreeHomeLeaf = "kiro"

// InTreeHome is the KIRO_HOME value for an in-tree AGENT run in workDir.
func InTreeHome(workDir string) string {
	return filepath.Join(StateHome(workDir), inTreeHomeLeaf)
}
