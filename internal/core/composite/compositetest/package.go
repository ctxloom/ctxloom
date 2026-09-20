package compositetest

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// PackageOption adds one item to a fixture package.
type PackageOption func(*composite.Package)

// WithFragment adds an unpremised fragment.
func WithFragment(name, body string) PackageOption {
	return func(p *composite.Package) {
		p.Fragments = append(p.Fragments, composite.Item[composite.Fragment]{Ref: "fixture#fragment/" + name, Value: composite.Fragment{Name: name, Body: body}})
	}
}

// WithPremised adds a PREFACE fragment: one with a premise.
func WithPremised(name, body, premise string) PackageOption {
	return func(p *composite.Package) {
		p.Premised = append(p.Premised, composite.Item[composite.Fragment]{Ref: "fixture#fragment/" + name, Value: composite.Fragment{Name: name, Body: body, Premise: premise}})
	}
}

// WithCommand adds a command carrying no per-engine block.
func WithCommand(name, body string) PackageOption {
	return func(p *composite.Package) {
		p.Commands = append(p.Commands, composite.Item[composite.Command]{Ref: "fixture#command/" + name, Value: composite.Command{Name: name, ExportName: name, Body: body}})
	}
}

// WithSkill adds a one-file skill package carrying no per-engine block.
func WithSkill(name string) PackageOption {
	return func(p *composite.Package) {
		p.Skills = append(p.Skills, composite.Item[composite.Skill]{Ref: "fixture#skill/" + name, Value: composite.Skill{
			Name:  name,
			Files: []engine.SkillFile{{Path: "SKILL.md", Bytes: []byte("# " + name + "\n"), Size: int64(len(name) + 3)}},
		}})
	}
}

// WithMCP adds a bundle MCP server.
func WithMCP(name string, server wire.MCPServer) PackageOption {
	return func(p *composite.Package) {
		if p.MCP == nil {
			p.MCP = map[string]wire.MCPServer{}
		}
		p.MCP[name] = server
	}
}

// Fixture is a composite.Package built from items directly — no bundle
// tree, no trust decision, no assembly — for a test that needs a package
// with a known shape and nothing else. The Context is the fragments'
// bodies joined, so a delivery of it has bytes to write.
func Fixture(t *testing.T, opts ...PackageOption) composite.Package {
	t.Helper()
	p := composite.Package{}
	for _, o := range opts {
		o(&p)
	}
	for _, f := range p.Fragments {
		if p.Context.Text != "" {
			p.Context.Text += "\n\n"
		}
		p.Context.Text += f.Value.Body
	}
	return p
}
