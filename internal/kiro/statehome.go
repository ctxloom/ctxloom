package kiro

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// inTreeHomeLeaf is the directory INSIDE the session's instance home
// (paths.SessionHomePath) that KIRO_HOME names. It equals the leaf the WORKTREE
// axis uses (internal/lm/isolation's credentialSeedSpecs["kiro"] KIRO_HOME
// HomeVar Subdir) so kiro's home layout is the same shape on every axis. The
// two literals live in different packages because isolation cannot import this
// one (kiro -> internal/acp -> internal/lm/isolation is a real cycle);
// TestSessionHome_MatchesTheIsolationHomeVarSubdir is the gate that keeps them
// equal.
const inTreeHomeLeaf = "kiro"

// SessionHome is the KIRO_HOME value for ONE SESSION's in-tree agent run in
// workDir — <workDir>/.ctxloom/state/<harp>/home/kiro.
//
// PER SESSION, not per project: the instance is created at instance time and is
// disposable, two concurrent sessions in one checkout get two homes, and the
// human's real ~/.kiro is never written by ctxloom.
//
// WHY NOT the real ~/.kiro, which an in-tree kiro run used before the
// controlled home existed: KIRO_HOME relocates kiro's WHOLE home (kiro-cli >=
// 2.3.0 — global agents, prompts, skills, steering, settings AND sessions), so
// an agent run pointed at the human's own home reads their global agents and
// steering and writes its own session state back into them. An agent is not the
// human.
//
// The rule is AGENT runs whose binding declares `config_home: project`: every
// other run keeps the real ~/.kiro. See
// docs/architecture/engines/isolation.md's engine config homes section, and
// operations.InTreeAgentHomeEnv, which is the one place that condition is
// decided.
//
// NO CREDENTIALS are copied in here and none can be: kiro's subscription auth
// lives in a global sqlite under XDGDataHomeEnv that KIRO_HOME does not
// relocate (see HomeEnv's doc, live-verified against kiro-cli 2.12.1), so a
// FRESH KIRO_HOME stays authenticated. This is why the in-tree arm relocates
// KIRO_HOME and deliberately leaves XDG_DATA_HOME alone: relocating the
// credential store with nothing to seed into it would strand the agent logged
// out, which is exactly what the worktree axis' GatedOnCreds machinery refuses
// to do. kiro's ambient set is DECLARED empty, not omitted.
//
// kiro's CWD-KEYED surfaces — .kiro/steering, .kiro/settings — are untouched by
// this and stay at the project root where kiro natively reads them.
//
// The error is harp validation (paths.SessionStatePath): a harpless caller
// cannot resolve an instance at all.
func SessionHome(workDir, harp string) (string, error) {
	root, err := paths.SessionHomePath(filepath.Join(workDir, paths.AppDirName), harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, inTreeHomeLeaf), nil
}
