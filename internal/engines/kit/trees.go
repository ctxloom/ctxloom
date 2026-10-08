package kit

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The managed trees: a commands or skills kind delivered as files under one
// directory the approach owns, through the shared writers
// (agent.WriteManagedCommandFiles, agent.WriteManagedSkillPackages), which
// skip a traversal or absolute name with a warning. The engine supplies only
// its content: a command's file form (render) and which skills it accepts.

// DeliverCommands writes each enabled command, rendered by the engine, under
// the managed commands directory of the root the plan selected (a.Rooted with
// homeRel / projectRel), and declares every file placed. announce, when set,
// names the directory on the engine's channel.
func DeliverCommands(a Approach, start present.Start, root present.RootKind, homeRel, projectRel string,
	files safefs.Root, in engine.CommandsInputs, render func(agent.CommandExport) (string, []byte, error),
	announce func(present.Rooted) present.Rooted) (present.Delivered, error) {
	p, err := managedDir(a, start, root, homeRel, projectRel, announce)
	if err != nil {
		return present.Delivered{}, err
	}
	cmds := make([]agent.CommandExport, 0, len(in.Commands))
	for _, c := range in.Commands {
		cmds = append(cmds, agent.CommandExport{
			Name: c.Name, Content: string(c.Body), Enabled: c.Enabled,
			Description: c.Description, ArgumentHint: c.ArgumentHint, AllowedTools: c.AllowedTools, Model: c.Model,
		})
	}
	placed, err := agent.WriteManagedCommandFiles(files, p.HostPath, cmds, render)
	if err != nil {
		return present.Delivered{}, err
	}
	return present.Delivered{Presented: p, Files: placed}, nil
}

// DeliverSkills writes each enabled skill package the engine accepts as
// <dir>/<name>/… under the managed skills directory of the root the plan
// selected, each file at its declared mode (0644 when undeclared), and
// declares every file placed. accept, when set, filters the exports to those
// the engine will load (AgentSkillRules-style constraints); nil accepts all.
func DeliverSkills(a Approach, start present.Start, root present.RootKind, homeRel, projectRel string,
	files safefs.Root, in engine.SkillsInputs, accept func([]agent.SkillExport) []agent.SkillExport,
	announce func(present.Rooted) present.Rooted) (present.Delivered, error) {
	p, err := managedDir(a, start, root, homeRel, projectRel, announce)
	if err != nil {
		return present.Delivered{}, err
	}
	skills := make([]agent.SkillExport, 0, len(in.Skills))
	for _, s := range in.Skills {
		e := agent.SkillExport{Name: s.Name, Description: s.Description, Enabled: s.Enabled}
		for _, f := range s.Files {
			mode := os.FileMode(f.Mode)
			if mode == 0 {
				mode = 0o644
			}
			e.Files = append(e.Files, agent.PackageFile{RelPath: f.Path, Content: f.Bytes, Mode: mode})
		}
		skills = append(skills, e)
	}
	if accept != nil {
		skills = accept(skills)
	}
	placed, err := agent.WriteManagedSkillPackages(files, p.HostPath, skills)
	if err != nil {
		return present.Delivered{}, err
	}
	return present.Delivered{Presented: p, Files: placed}, nil
}

// managedDir is the managed tree's directory under the selected root,
// announced when the engine names it on a channel.
func managedDir(a Approach, start present.Start, root present.RootKind, homeRel, projectRel string, announce func(present.Rooted) present.Rooted) (present.Presentation, error) {
	r, err := a.Rooted(start, root, homeRel, projectRel)
	if err != nil {
		return present.Presentation{}, err
	}
	if announce != nil {
		r = announce(r)
	}
	return r.Build(), nil
}
