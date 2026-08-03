// The "confirm by repeating" override: the one part of the decision path that
// is STATEFUL. A denial the rule marks confirmable is lifted when the agent
// repeats the same command (or the same file edit) inside the rule's window,
// which needs a state file on disk, a clock, and a key derived from what was
// repeated. Only the hook applies it — `check` reports what the rules say and
// nothing stateful — so it lives apart from the wiring the two commands share.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/state"
)

// applyConfirmOverride lifts a denial that "confirm by repeating" permits.
// Only a rule that allows it qualifies — inviolate rules report
// Confirmable=false, so repeating them never helps — and the override is keyed
// on what the agent repeats: the command, or the file path for a file-edit
// rule.
func applyConfirmOverride(resp engine.Response, req engine.Request, resolvedConfig string) engine.Response {
	if resp.Allow || !resp.Confirmable || resp.ConfirmWindowSeconds <= 0 {
		return resp
	}
	key := req.Command
	if req.FilePath != "" {
		key = "edit:" + req.FilePath
	}
	return confirmByRepeat(resp, key, statePath(resolvedConfig),
		time.Duration(resp.ConfirmDelaySeconds)*time.Second,
		time.Duration(resp.ConfirmWindowSeconds)*time.Second)
}

// confirmByRepeat applies the "run it again to permit" override (state.ConfirmByRepeat)
// using the wall clock, and notes on stderr when a repeat was honored. The logic
// lives in internal/state so the acceptance suite can exercise it too.
func confirmByRepeat(resp engine.Response, command, stateFile string, delay, window time.Duration) engine.Response {
	out, overridden, err := state.ConfirmByRepeat(afero.NewOsFs(), resp, command, stateFile, time.Now(), delay, window)
	if overridden {
		fmt.Fprintln(os.Stderr, progName+": command repeated within the override window — allowing.")
	}
	if err != nil {
		// A read or persistence failure here used to vanish silently. Either
		// way the override never survives to the next invocation, so every
		// future identical repeat keeps getting denied with a message
		// promising a repeat WOULD work — or, on the read side, the operator's
		// live overrides evaporate with nothing to look at. Diagnostic only;
		// the decision above is unchanged.
		fmt.Fprintf(os.Stderr, "%s: could not read or persist confirm-by-repeat state (%v) — the override may not take effect\n", progName, err)
	}
	return out
}

// statePath puts the override state in a .ltk directory anchored to the
// resolved config: next to the config when it already lives in a .ltk
// directory, otherwise in a .ltk/ beside the config file (legacy flat configs,
// custom --config paths). Anchoring to the config — never the cwd — keeps
// confirm-by-repeat working when the host varies the hook cwd (agy runs hooks
// in <workspace>/.agents; the config search walks up, so the resolved path is
// the stable anchor). Runtime state always lives inside a .ltk directory,
// never loose in the project root, so .gitignore's ".ltk/state.json" entry
// covers it.
//
// With no config at all there is nothing to anchor to, and also nothing to
// confirm (no rules ⇒ no confirmable denial), so the cwd-relative fallback is
// effectively unreachable on the decision path.
func statePath(configPath string) string {
	if configPath == "" {
		return filepath.Join(configDir, stateBase)
	}
	dir := filepath.Dir(configPath)
	if filepath.Base(dir) == configDir {
		return filepath.Join(dir, stateBase)
	}
	return filepath.Join(dir, configDir, stateBase)
}
