package delivery

import (
	"slices"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// ProjectPlan routes items for an AT-REST delivery into a project root:
// every kind the engine's approach offers there is selected there; a kind
// the engine does not carry is an accepted loss the caller reports. A kind
// the engine carries but offers nowhere the project target has is
// Unrootable — refused with the remedy, never rerouted. A caller that wants
// a kind left out (the context an engine reads through its session-start
// hook) hands over items without it.
func ProjectPlan(root engine.Base, items engine.Items, dir string) (Plan, error) {
	pref := Preference{Root: map[present.Kind]present.RootKind{}, AcceptLoss: map[present.Kind]bool{}}
	surfaces := root.Surfaces()
	for _, kind := range []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills} {
		a, carried := surfaces[kind]
		switch {
		case !carried:
			pref.AcceptLoss[kind] = true
		case a.Traits().Offers(present.RootProjectRoot):
			pref.Root[kind] = present.RootProjectRoot
		}
	}
	return Route(items, root, pref, present.ProjectOnHost(dir).Paths())
}

// ProjectTarget is the at-rest target: the project root under the project
// writer, recorded in records.
func ProjectTarget(dir string, records Ownership) Target {
	return Target{Root: present.ProjectOnHost(dir), Ownership: records, Writer: ProjectWriter}
}

// ProjectClaims is the record's account of what the project writer has
// installed in a file: the places it claims that the file holds now. It is
// what a settings status reads (the agent.SettingsOptions field of the same
// name).
func ProjectClaims(fs afero.Fs, records Ownership) func(target string) ([]string, error) {
	return func(target string) ([]string, error) {
		places, err := records.Paths(fs, target)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, p := range places {
			if p.Live && slices.Contains(p.Writers, ProjectWriter) {
				out = append(out, p.Pointer)
			}
		}
		return out, nil
	}
}
