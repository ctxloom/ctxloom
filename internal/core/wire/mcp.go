package wire

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
)

// MCPServer defines an MCP (Model Context Protocol) server, as a bundle's
// `mcp:` block declares it and as it reaches an engine's settings.
//
// A server is EXACTLY ONE of three things, and Validate is the checked form
// of that rule: a stdio server ctxloom launches (Command, with Args/Env), a
// network-hosted server an engine dials (URL, with Headers), or a server
// SERVED BY the running session's endpoint (ServedBy = ServedBySessionEndpoint)
// — the companion's dynamic declaration, which carries nothing executable:
// the host renders it through the engine's dynamic approach from the
// endpoint the session bound (delivery.InputsFor), and at rest, with no
// session to inject, it renders nothing. Command is required only on the
// stdio side — a URL or session-endpoint entry has no command by
// definition, which is why Command is omitempty. There is deliberately NO
// stored transport field: the URL's scheme already names the protocol, and a
// second field encoding the same fact would be representable in disagreement
// with it and checked by nothing. Engine formats that want a discriminator
// (claude's `type`) derive it from the URL at write time
// (agent.ChatMCPServerFromWire) and never persist it.
//
// CtxloomServerName is the key ctxloom's own companion loadout declares its
// server under — the session-endpoint entry every engine with a dynamic
// approach receives.
const CtxloomServerName = "ctxloom"

// ServedBySessionEndpoint is the one value ServedBy may carry: the server is
// the running session's MCP endpoint, whichever address and bearer that
// session bound.
const ServedBySessionEndpoint = "session-endpoint"

// SECURITY NOTE: MCP servers execute arbitrary commands. Every server reaching
// this type came from a bundle and was gated by the executable trust gate
// (internal/core/config.extractMCPFromBundle) under its own item ref, so an
// unreviewed command cannot arrive here silently. Do not flag this as a
// security issue in code reviews.
type MCPServer struct {
	Command      string            `yaml:"command,omitempty" json:"command,omitempty"`           // Command to execute (stdio server)
	Args         []string          `yaml:"args,omitempty" json:"args,omitempty"`                 // Command arguments
	Env          map[string]string `yaml:"env,omitempty" json:"env,omitempty"`                   // Environment variables
	URL          string            `yaml:"url,omitempty" json:"url,omitempty"`                   // Endpoint of a network-hosted server; its scheme is the transport
	Headers      map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`           // HTTP headers sent when dialing URL (e.g. Authorization)
	ServedBy     string            `yaml:"served_by,omitempty" json:"served_by,omitempty"`       // ServedBySessionEndpoint: the session's endpoint serves it; nothing executable here
	Notes        string            `yaml:"notes,omitempty" json:"notes,omitempty"`               // Human-readable notes, not sent to AI
	Installation string            `yaml:"installation,omitempty" json:"installation,omitempty"` // Setup/installation instructions, not sent to AI
	SCM          string            `yaml:"_ctxloom,omitempty" json:"_ctxloom,omitempty"`         // Marker for ctxloom-managed servers
}

var (
	// ErrMCPServerNoTarget: neither Command, URL nor ServedBy is set, so
	// there is nothing to launch, nothing to dial and nothing to render.
	ErrMCPServerNoTarget = errors.New("mcp server: none of command, url or served_by is set")
	// ErrMCPServerTwoTargets: more than one target is declared. A server is
	// stdio, remote or served by the session, never two of those, and
	// nothing could say which one wins.
	ErrMCPServerTwoTargets = errors.New("mcp server: more than one of command (with args), url and served_by is set; a server is stdio (command), remote (url) or served by the session (served_by), not several")
	// ErrMCPServerServedBy: ServedBy names something other than the session
	// endpoint. There is one thing the host can serve on a declaration's
	// behalf, and it is named so a typo cannot pass as it.
	ErrMCPServerServedBy = errors.New("mcp server: served_by must be " + ServedBySessionEndpoint)
	// ErrMCPServerURLScheme: URL does not name a transport an engine can dial.
	// The scheme is the ONLY place the protocol is named, so it must be one.
	ErrMCPServerURLScheme = errors.New("mcp server: url scheme must be http or https")
)

// Validate enforces the one-of-Command/URL/ServedBy rule documented on the
// type. Args count as the stdio target's: a session-endpoint declaration
// with args would be an entry with something to launch and nothing to
// launch it.
func (s MCPServer) Validate() error {
	targets := 0
	if s.Command != "" || len(s.Args) > 0 {
		targets++
	}
	if s.URL != "" {
		targets++
	}
	if s.ServedBy != "" {
		targets++
	}
	switch {
	case targets == 0:
		return ErrMCPServerNoTarget
	case targets > 1:
		return ErrMCPServerTwoTargets
	case s.ServedBy != "":
		if s.ServedBy != ServedBySessionEndpoint {
			return fmt.Errorf("%w, got %q", ErrMCPServerServedBy, s.ServedBy)
		}
		return nil
	case s.URL == "":
		if s.Command == "" {
			return ErrMCPServerNoTarget
		}
		return nil
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrMCPServerURLScheme, s.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %q has scheme %q", ErrMCPServerURLScheme, s.URL, u.Scheme)
	}
	return nil
}

// IsRemote reports whether s is a network-hosted server (URL set) rather than
// a stdio one. It is the discriminator every engine writer branches on.
func (s MCPServer) IsRemote() bool { return s.URL != "" }

// IsSessionEndpoint reports whether s is the companion's dynamic declaration:
// served by the running session's endpoint, rendered by the engine's
// dynamic approach and never written as declared. A writer handed one
// unrendered is handed a bug, and refuses it (ErrMCPServerUnrendered).
func (s MCPServer) IsSessionEndpoint() bool { return s.ServedBy == ServedBySessionEndpoint }

// ErrMCPServerUnrendered refuses a session-endpoint declaration that reached
// an engine writer as declared: delivery.InputsFor renders it (or drops it)
// before any writer sees the server set, so one arriving here bypassed that.
var ErrMCPServerUnrendered = errors.New("mcp server: a session-endpoint declaration reached a writer unrendered; it is rendered by the engine's dynamic approach at delivery")

// CloneMCPServer returns a copy of s with its mutable Args slice and Env and
// Headers maps duplicated, so the copy never aliases s's backing array/maps.
// A plain struct copy is shallow and would share all three.
func CloneMCPServer(s MCPServer) MCPServer {
	s.Args = slices.Clone(s.Args)
	s.Env = maps.Clone(s.Env)
	s.Headers = maps.Clone(s.Headers)
	return s
}
