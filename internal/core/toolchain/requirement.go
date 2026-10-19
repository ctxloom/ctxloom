// Package toolchain is the pure vocabulary for what an agent needs installed
// where it runs: a command, and every reason the agent has to want it. It does
// no resolution of its own; the adapters that know how an agent composes
// bundles and MCP servers feed a Collector.
package toolchain

import (
	"sort"
)

// Source kinds: where a requirement came from.
const (
	// KindBundle is a `requires.commands` entry of a composed bundle; Ref is
	// the bundle's reference as a profile names it.
	KindBundle = "bundle"
	// KindMCP is the launcher of an attached stdio MCP server; Ref is the
	// server's name.
	KindMCP = "mcp"
)

// RequirementSource names one reason a command is required.
type RequirementSource struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// String renders the source as "<kind> <ref>", the form doctor and agent show
// print.
func (s RequirementSource) String() string { return s.Kind + " " + s.Ref }

// Requirement is one required command with every source that asks for it.
type Requirement struct {
	Command string              `json:"command"`
	Sources []RequirementSource `json:"sources"`
}

// Collector accumulates requirements, de-duplicating by command and keeping
// every distinct source. The zero value is ready to use.
type Collector struct {
	sources map[string]map[RequirementSource]bool
}

// Add records that src requires command.
func (c *Collector) Add(command string, src RequirementSource) {
	if c.sources == nil {
		c.sources = map[string]map[RequirementSource]bool{}
	}
	if c.sources[command] == nil {
		c.sources[command] = map[RequirementSource]bool{}
	}
	c.sources[command][src] = true
}

// Requirements returns what was collected, sorted by command, each command's
// sources sorted by kind then ref, so the result is stable across runs.
func (c *Collector) Requirements() []Requirement {
	out := make([]Requirement, 0, len(c.sources))
	for command, set := range c.sources {
		req := Requirement{Command: command, Sources: make([]RequirementSource, 0, len(set))}
		for src := range set {
			req.Sources = append(req.Sources, src)
		}
		sort.Slice(req.Sources, func(i, j int) bool {
			a, b := req.Sources[i], req.Sources[j]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			return a.Ref < b.Ref
		})
		out = append(out, req)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Command < out[j].Command })
	return out
}

// Commands lists the command names of reqs, in order.
func Commands(reqs []Requirement) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Command
	}
	return out
}
