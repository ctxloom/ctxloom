package claude

import (
	"errors"
	"maps"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// This file holds the pieces of claude's delivery its typed approaches
// (definition.go) share: the approach names a binding selects
// (ApproachSystemPrompt, ApproachMCPConfig — claude's own, declared here and
// nowhere shared), the private-root seam every session-home approach reads,
// the framed system-prompt writer the context approach drives, and the relay
// bearer's by-reference rewrite for a project .mcp.json.

// ApproachSystemPrompt names claude's out-of-cwd framed context consumed via
// --append-system-prompt-file — semantically distinct from the native file
// (the content enters the system prompt, not project memory). It is claude's
// own name, declared here and nowhere shared: no other engine has it.
const ApproachSystemPrompt = "system-prompt"

// placement is where a file-writing strategy writes: appendFlagDelivery holds
// one, injected at construction, never passed as a method parameter.
type placement interface {
	// Dir returns the directory the strategy writes into.
	Dir() string
}

// dirPlacement is a trivial placement whose Dir() returns a fixed directory.
// It adapts a root read from the advised Start into the placement
// appendFlagDelivery constructs against; the root arrives at call time, never
// at construction.
type dirPlacement struct{ dir string }

// Dir returns the fixed directory this placement wraps.
func (p dirPlacement) Dir() string { return p.dir }

// privateRoot is the ONE place claude decides which advised root a run's
// CONFIGURATION lands under — the framed system prompt and the default
// .mcp.json, neither of which may be written into the shared live cwd.
// Every such approach reads it rather than naming a root itself, so the
// choice is made once and cannot drift between two surfaces that are supposed
// to obey one rule.
//
// It is the run's SESSION HOME, the relocated engine home: configuration is a
// property of the run, mounted into the container and inited against, and
// the home is resolved off the agent binding alone — orthogonal to the cell,
// so every cell kind reaches the same root.
//
// Only a binding that relocates the home (engine_home: session) advises this
// root at all. A run without one is REFUSED by every approach beneath it
// (privateRooted) rather than served from the user's real home, which is
// shared across every session and exactly the file these approaches exist to
// stay out of.
func privateRoot(start present.Start) present.Root { return start.Paths().SessionHome }

// underPrivateRoot roots a PRESENTATION at rel beneath the same root
// privateRoot names. It sits here, adjacent to privateRoot and nowhere else,
// because the two must move together: the presenter states where the bytes go
// and Deliver puts them there, so a flip that changed one and not the other
// would announce a path nothing was written to. They are checked against each
// other by TestSurfaces_PresentedPathIsWhereTheApproachWrites.
func underPrivateRoot(start present.Start, rel string) present.Rooted {
	return start.UnderSessionHome(rel)
}

// privateRooted is the entry refusal for an approach that lands beneath
// privateRoot: the seam's third member, kept adjacent for the same reason as
// underPrivateRoot. The refusal is root-specific — each advised root has its
// own sentinel naming its own remedy — so a flip that moved the placement and
// left the check on the old root would refuse runs that HAVE the new root and
// serve runs that lack it, with a bare relative path.
func privateRooted(start present.Start) error { return agent.SessionHomeRooted(start) }

// systemPromptContext is claude's system-prompt context approach.
//
// It writes the framed <hash>.sysprompt.md beneath the run's private root via
// the existing appendFlagDelivery and exposes its path (Path) for
// --append-system-prompt-file.
//
// It has exactly ONE form, on every cell. It previously had two, and they were
// named backwards from the cell that ran them: the plain Deliver was the
// CLAUDE.md write — the same write the native-file approach performs — so an
// ISOLATED launch (worktree or container) that had selected system-prompt was
// silently handed project memory instead, while only a SHARED launch got the
// framed file. The content reaches the engine by a different mechanism under a
// different name, which is a different product behaviour, not a placement
// detail.
//
// The substitution is gone rather than redirected, because a surface is the
// wrong place to decide one: the system prompt does not know what it would be
// degrading to, or whether the caller would have accepted CLAUDE.md instead.
// That is the engine declaration's job. A run that cannot serve this approach
// gets ErrUnrootedSessionHome and writes nothing.
type systemPromptContext struct {
	content string
	fs      afero.Fs
	path    string // set by Deliver: the framed context file under the private root
}

// Present declares the out-of-cwd form's flag, naming the basename Deliver
// already wrote at Path() — appendFlagDelivery names the file
// <hash>.sysprompt.md where <hash> is a sha256 prefix over the FRAMED BYTES,
// so the leaf is unknowable until Deliver has run, and Present reads it back
// from Path() rather than recomputing it. filepath.Base(s.path) rather than
// s.path itself, because Path() is a HOST path and underPrivateRoot performs
// the same host/engine root mapping every other surface's Present goes
// through (container-rootless/rootful differ there); only the leaf is
// specific to this file, the root is not.
//
// s.path == "" is Path()'s own no-file contract — before delivery, for empty
// context, and after a FAILED delivery — and every one of those is a case
// with no written file behind it. Announcing the bare private root as the
// flag's value would violate Deliver's invariant that no flag may name a
// file that was not written, so this presents nothing at all: an unrooted
// Presentation{} for the caller to skip.
func (s *systemPromptContext) Present(start present.Start) present.Presentation {
	if s.path == "" {
		return present.Presentation{}
	}
	return underPrivateRoot(start, filepath.Base(s.path)).AnnounceFlag(flagAppendSystemFile).Build()
}

// Deliver writes the framed context file through the reused appendFlagDelivery
// beneath the advised private root; Path then exposes it for
// --append-system-prompt-file. This is the approach's ONLY form, so every cell
// reaches it — an isolated launch that selected system-prompt now gets the
// system prompt.
//
// An unresolved private root REFUSES (ErrUnrootedSessionHome) rather than writing
// the well-known file instead; see the type doc. A FAILED write leaves Path ""
// (the writer's own contract): no flag may name a file that was not written.
func (s *systemPromptContext) Deliver(start present.Start) (agent.Delivered, error) {
	if err := privateRooted(start); err != nil {
		return nil, err
	}
	d := newAppendFlagDelivery(dirPlacement{dir: privateRoot(start).Host}, s.fs)
	handle, err := d.DeliverContext(s.content)
	s.path = d.Path()
	return handle, err
}

// Path returns the framed <hash>.sysprompt.md written by Deliver (for
// --append-system-prompt-file), or "" whenever no file stands behind it: before
// delivery, for empty context, and after a FAILED delivery.
func (s *systemPromptContext) Path() string { return s.path }

// ApproachMCPConfig names claude's PRIVATE MCP config file, announced on
// --mcp-config. It is claude's own name, declared here and nowhere shared, and
// it is the DEFAULT: a run's MCP set is ctxloom's to deliver, and delivering it
// by writing the user's project .mcp.json is a shared/dangerous avenue nothing
// should take by default.
const ApproachMCPConfig = "mcp-config"

// relayBearerRef is how the project .mcp.json names the relay's bearer.
var relayBearerRef = "${" + EnvRelayBearer + "}"

// errTwoRelayBearers refuses a server set naming two different relay
// bearers: claude's environment holds one value per name.
var errTwoRelayBearers = errors.New("claude: two MCP entries carry different relay bearers, and claude's environment can hold only one")

// bearerByReference returns bundle with every relay bearer value replaced by
// relayBearerRef, and the value it replaced, keyed by its variable. The
// caller's servers are not modified.
func bearerByReference(bundle map[string]wire.MCPServer) (map[string]wire.MCPServer, map[string]string, error) {
	out := make(map[string]wire.MCPServer, len(bundle))
	var env map[string]string
	for name, srv := range bundle {
		v, ok := srv.Env[EnvRelayBearer]
		if !ok || v == relayBearerRef {
			out[name] = srv
			continue
		}
		if prev, seen := env[EnvRelayBearer]; seen && prev != v {
			return nil, nil, errTwoRelayBearers
		}
		env = map[string]string{EnvRelayBearer: v}
		srv.Env = maps.Clone(srv.Env)
		srv.Env[EnvRelayBearer] = relayBearerRef
		out[name] = srv
	}
	return out, env, nil
}

// Compile-time contract.
var _ placement = dirPlacement{}
