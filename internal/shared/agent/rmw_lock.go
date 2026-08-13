package agent

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// WithFileLock runs fn as ONE serialized read-modify-write transaction
// against target — an engine-owned settings file OUTSIDE any .ctxloom tree
// (a real home ~/.claude/settings.json, a project's .mcp.json,
// ~/.codex/config.toml, .kiro/settings/mcp.json, opencode.json, ...). It is
// the SettingsWriter family's counterpart to config.Manager.Update: hooks
// (SessionStart et al.), the MCP server, the CLI, the runner, and an
// in-container ctxloom (the same files bind-mounted) all read-modify-write
// these files, genuinely concurrently and today unlocked — two racing RMWs
// is a lost update on a file ctxloom does not own.
//
// fn is the WHOLE cycle — read, modify, write — never just the write: the
// lock has to be held before the read for "fresh" to mean anything. Every
// call site wraps its existing body (which already does its own read as the
// first real step) rather than splitting out a separate unlocked
// path-resolution phase the way config.Manager.Update does — these targets
// are deterministic paths derived from arguments already in hand, so there
// is nothing upstream of the read that needs to run before the lock exists.
//
// filelock.PathFor (a beside-file ".lock" sidecar), never ProjectPathFor:
// every target this guards lives outside a project .ctxloom tree, where
// ProjectPathFor would refuse to derive a location at all.
//
// A lock ACQUISITION failure fails the whole call closed, matching
// config.Manager.Update's stance verbatim: filelock.Lock only errors on a
// persistent environmental failure (never ordinary contention, which it
// already waits out), so proceeding unlocked on that failure would silently
// discard the one guarantee this function exists to provide. The target file
// is left untouched on that path — fn never runs.
func WithFileLock(target string, fn func() error) error {
	lockPath := filelock.PathFor(target)
	unlock, err := filelock.Lock(lockPath)
	if err != nil {
		return fmt.Errorf("agent: acquiring settings lock for %s: %w", target, err)
	}
	defer unlock()
	return fn()
}
