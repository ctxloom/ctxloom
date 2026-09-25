package engine_test

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// anyApproach satisfies every kind's approach interface and the dynamic
// one, named so a test can tell which field it came from.
type anyApproach struct{ name string }

func (a anyApproach) Name() string                            { return a.name }
func (anyApproach) Traits() present.Traits                    { return present.Traits{} }
func (anyApproach) Endpoint(sessions.Endpoint) wire.MCPServer { return wire.MCPServer{} }
func (anyApproach) DeliverContext(present.Start, present.RootKind, engine.ContextInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (anyApproach) DeliverMCP(present.Start, present.RootKind, engine.MCPInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (anyApproach) DeliverSettings(present.Start, present.RootKind, engine.SettingsInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (anyApproach) DeliverHooks(present.Start, present.RootKind, engine.HooksInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (anyApproach) DeliverCommands(present.Start, present.RootKind, engine.CommandsInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (anyApproach) DeliverSkills(present.Start, present.RootKind, engine.SkillsInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}

// TestBase_Surfaces_WalksEveryTypedField pins the typed-field walk: each
// declared approach appears under its own kind, and an undeclared one is
// omitted.
func TestBase_Surfaces_WalksEveryTypedField(t *testing.T) {
	full := engine.Base{Definition: engine.Definition{
		Context: anyApproach{"context"}, MCP: anyApproach{"mcp"}, Settings: anyApproach{"settings"},
		Hooks: anyApproach{"hooks"}, Commands: anyApproach{"commands"}, Skills: anyApproach{"skills"},
	}}
	got := map[present.Kind]string{}
	for k, a := range full.Surfaces() {
		got[k] = a.Name()
	}
	assert.Equal(t, map[present.Kind]string{
		present.Context: "context", present.MCP: "mcp", present.Settings: "settings",
		present.Hooks: "hooks", present.Commands: "commands", present.Skills: "skills",
	}, got)
	assert.Empty(t, engine.Base{}.Surfaces())
	assert.Equal(t, []present.Kind{present.Skills}, engine.Base{Definition: engine.Definition{Skills: anyApproach{"skills"}}}.Static())
}

// TestBase_Delegate_RoutesPrefaceItemsDynamicAndTheRestStatic pins the
// common decisioning: premised fragments go dynamic only when the engine
// has a dynamic approach, every other item kind is static in kind order,
// and a dynamic approach always makes MCP static (the endpoint's entry).
func TestBase_Delegate_RoutesPrefaceItemsDynamicAndTheRestStatic(t *testing.T) {
	everything := engine.Items{
		Fragments: []engine.FragmentItem{{Ref: "plain"}, {Ref: "cond", Premise: "when x"}},
		Commands:  []engine.CommandItem{{}},
		Skills:    []engine.SkillItem{{}},
		Hooks:     []wire.Hook{{}},
		MCP:       []wire.MCPServer{{}},
		Settings:  true,
	}
	allKinds := []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills}
	dynamic := engine.Base{Definition: engine.Definition{Dynamic: anyApproach{"dyn"}}}

	cases := []struct {
		name  string
		base  engine.Base
		items engine.Items
		want  engine.Delegation
	}{
		{"static engine, everything", engine.Base{}, everything, engine.Delegation{Static: allKinds}},
		{"dynamic engine, everything", dynamic, everything, engine.Delegation{Static: allKinds, Dynamic: []string{"cond"}}},
		{"static engine, only a premised fragment", engine.Base{}, engine.Items{Fragments: []engine.FragmentItem{{Ref: "cond", Premise: "p"}}},
			engine.Delegation{Static: []present.Kind{present.Context}}},
		{"dynamic engine, only a premised fragment", dynamic, engine.Items{Fragments: []engine.FragmentItem{{Ref: "cond", Premise: "p"}}},
			engine.Delegation{Static: []present.Kind{present.MCP}, Dynamic: []string{"cond"}}},
		{"static engine, nothing", engine.Base{}, engine.Items{}, engine.Delegation{}},
		{"dynamic engine, nothing", dynamic, engine.Items{}, engine.Delegation{Static: []present.Kind{present.MCP}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.base.Delegate(tc.items))
		})
	}
}
