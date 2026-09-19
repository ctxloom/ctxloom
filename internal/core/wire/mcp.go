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
// A server is EXACTLY ONE of two things, and Validate is the checked form of
// that rule: a stdio server ctxloom launches (Command, with Args/Env), or a
// network-hosted server an engine dials (URL, with Headers). Command is
// required only on the stdio side — a URL entry has no command by definition,
// which is why Command is omitempty. There is deliberately NO stored
// transport field: the URL's scheme already names the protocol, and a second
// field encoding the same fact would be representable in disagreement with
// it and checked by nothing. Engine formats that want a discriminator
// (claude's `type`) derive it from the URL at write time
// (agent.ChatMCPServerFromWire) and never persist it.
//
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
	Notes        string            `yaml:"notes,omitempty" json:"notes,omitempty"`               // Human-readable notes, not sent to AI
	Installation string            `yaml:"installation,omitempty" json:"installation,omitempty"` // Setup/installation instructions, not sent to AI
	SCM          string            `yaml:"_ctxloom,omitempty" json:"_ctxloom,omitempty"`         // Marker for ctxloom-managed servers
}

var (
	// ErrMCPServerNoTarget: neither Command nor URL is set, so there is
	// nothing to launch and nothing to dial.
	ErrMCPServerNoTarget = errors.New("mcp server: neither command nor url is set")
	// ErrMCPServerTwoTargets: Command and URL are both set. A server is stdio
	// or remote, never both, and nothing could say which one wins.
	ErrMCPServerTwoTargets = errors.New("mcp server: command and url are both set; a server is stdio (command) or remote (url), not both")
	// ErrMCPServerURLScheme: URL does not name a transport an engine can dial.
	// The scheme is the ONLY place the protocol is named, so it must be one.
	ErrMCPServerURLScheme = errors.New("mcp server: url scheme must be http or https")
)

// Validate enforces the one-of-Command/URL rule documented on the type.
func (s MCPServer) Validate() error {
	switch {
	case s.Command == "" && s.URL == "":
		return ErrMCPServerNoTarget
	case s.Command != "" && s.URL != "":
		return ErrMCPServerTwoTargets
	case s.URL == "":
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

// CloneMCPServer returns a copy of s with its mutable Args slice and Env and
// Headers maps duplicated, so the copy never aliases s's backing array/maps.
// A plain struct copy is shallow and would share all three.
func CloneMCPServer(s MCPServer) MCPServer {
	s.Args = slices.Clone(s.Args)
	s.Env = maps.Clone(s.Env)
	s.Headers = maps.Clone(s.Headers)
	return s
}
