// Package layerscope states which config LAYER may set which config KEY, and
// why: a config value is a fact about a machine, a user, a project, or one
// invocation, and a layer that cannot carry that fact must not set it.
//
// ctxloom merges config across four layers — home < project < ENV <
// --config-set — and merges EVERY key at EVERY layer; nothing downstream can
// tell which layer a value came from once the layers are merged. Three
// escalation paths follow directly: an env var can set a per-project human
// consent flag (dirty_tree_commit_ack) that its own doc says must be read
// "ONLY from the PROJECT config"; two env vars can mint a coordinator-trusted,
// permission-bypassing agent in a project that declares no such agent; and a
// home config can silently contribute permissions/coordinator/runtime fields
// into a project's same-named agent, producing a binding neither file
// describes. This package is the table that closes all three by
// construction: every key the schema knows is assigned a Scope, and a Scope
// names exactly which Layers may carry it.
package layerscope

import "github.com/ctxloom/ctxloom/internal/paths"

// Layer is one rung of the config resolution chain, in ASCENDING precedence
// (a higher Layer's value wins the merge) — see internal/shared/confload's
// package doc for the chain itself. Layer is not about precedence here,
// though: it is about REACH — home and project are files, env is inherited by
// every child process ctxloom spawns (including an engine an agent drives),
// and --config-set crosses no process boundary at all. Scope.Allows keys its
// whole policy on that difference.
type Layer uint8

const (
	// LayerHome is ~/.ctxloom/config.yaml — this USER, on this MACHINE.
	LayerHome Layer = iota
	// LayerProject is <project>/.ctxloom/config.yaml — COMMITTED and
	// multi-author: gitignore.PrivateStatePatterns deliberately excludes it,
	// so anything written here arrives, pre-set, in every clone.
	LayerProject
	// LayerEnv is CTXLOOM_CONFIG_* — ambient and inherited by every child
	// process this one spawns (each honoring its own EnvPrefix), including a
	// delegated engine an agent drives. An agent that can run `bash` can write
	// this layer for anything it reaches.
	LayerEnv
	// LayerFlag is --config-set <path>=<value> — scoped to ONE invocation;
	// crosses no process boundary (a child parses its own argv, which never
	// contains the parent's flags).
	LayerFlag
)

// String names l for a diagnostic.
func (l Layer) String() string {
	switch l {
	case LayerHome:
		return "home"
	case LayerProject:
		return "project"
	case LayerEnv:
		return "env"
	case LayerFlag:
		return "flag"
	default:
		return "unknown"
	}
}

// File reports the path a user must edit to set a key at this layer, or "" for
// the two override layers (env/flag cross no file at all) — what a
// Violation's FixIt names as the place to move a disallowed value TO, and what
// a Violation's Message names as the place the offending value came FROM.
// appPath is the project's .ctxloom directory; homeAppPath is the user's
// (empty when unresolvable, in which case LayerHome degrades to "").
func (l Layer) File(appPath, homeAppPath string) string {
	switch l {
	case LayerHome:
		if homeAppPath == "" {
			return ""
		}
		return paths.ConfigPath(homeAppPath)
	case LayerProject:
		return paths.ConfigPath(appPath)
	default:
		return ""
	}
}
