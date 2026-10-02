package mock

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The mock's native surfaces: every kind is a FILE the mock reads, under
// whichever root the plan selected — the context file at the root, the rest
// under its config dir. A test asserts what was delivered by reading the
// file, without an engine binary; the mock's own turn reads the hooks file
// to fire hooks (hooks.go).

// ContextFileName is the mock's well-known context file, at the root.
const ContextFileName = "MOCK_CONTEXT.md"

// The remaining surfaces, relative to the root.
const (
	mcpRel      = ConfigDirName + "/mcp.json"
	settingsRel = ConfigDirName + "/settings.json"
	hooksRel    = ConfigDirName + "/hooks.json"
	commandsRel = ConfigDirName + "/commands"
	skillsRel   = ConfigDirName + "/skills"
)

// The argv flags the mock's presentations announce their files on; the
// mock's CLI grammar declares both. HooksFlag is exported for the mock
// binary's interactive loop (mock/runtime), which reads the delivered hook
// file off its own argv to fire turn_start per typed line.
const (
	contextFlag = "--context"
	HooksFlag   = "--hooks"
)

// surface is the shared half of every mock approach: its name and traits.
type surface struct {
	name   string
	traits present.Traits
}

func (s *surface) Name() string           { return s.name }
func (s *surface) Traits() present.Traits { return s.traits }

// rooted composes rel under the root the plan selected; the approach
// offers exactly the roots its traits list, so any other is a refusal.
func (s *surface) rooted(start present.Start, root present.RootKind, rel string) (present.Rooted, error) {
	if !s.traits.Offers(root) {
		return present.Rooted{}, fmt.Errorf("mock/%s: root %v is not one this approach offers", s.name, root)
	}
	if root == present.RootProjectRoot {
		return start.UnderProjectRoot(rel), nil
	}
	return start.UnderSessionHome(rel), nil
}

// writeFile writes bytes at the composed presentation's host path.
func writeFile(fs afero.Fs, p present.Presentation, bytes []byte, mode os.FileMode) (present.Delivered, error) {
	if err := fs.MkdirAll(filepath.Dir(p.HostPath), 0o755); err != nil {
		return present.Delivered{}, err
	}
	if err := safefs.WriteFile(fs, p.HostPath, bytes, mode); err != nil {
		return present.Delivered{}, err
	}
	return present.Delivered{Presented: p, Wrote: []string{p.HostPath}, Undo: func(fs afero.Fs) error { return fs.Remove(p.HostPath) }}, nil
}

// contextFile appends the assembled context to MOCK_CONTEXT.md — after
// whatever a human already wrote there, which stays theirs — and announces
// the file on --context.
type contextFile struct{ surface }

func (a *contextFile) DeliverContext(start present.Start, root present.RootKind, in engine.ContextInputs, fs afero.Fs) (present.Delivered, error) {
	r, err := a.rooted(start, root, ContextFileName)
	if err != nil {
		return present.Delivered{}, err
	}
	p := r.AnnounceFlag(contextFlag).Build()
	// A context file the mock creates is owner-only: the engine reads it
	// itself and nothing else needs to. One that already stood keeps its
	// mode.
	if err := safefs.AppendSection(fs, p.HostPath, in.Text, 0o600); err != nil {
		return present.Delivered{}, err
	}
	return present.Delivered{Presented: p, Wrote: []string{p.HostPath}, Undo: func(fs afero.Fs) error { return fs.Remove(p.HostPath) }}, nil
}

// mcpFile writes the server set as {"mcpServers": {...}}.
type mcpFile struct{ surface }

func (a *mcpFile) DeliverMCP(start present.Start, root present.RootKind, in engine.MCPInputs, fs afero.Fs) (present.Delivered, error) {
	r, err := a.rooted(start, root, mcpRel)
	if err != nil {
		return present.Delivered{}, err
	}
	bytes, err := json.MarshalIndent(map[string]any{"mcpServers": in.Servers}, "", "  ")
	if err != nil {
		return present.Delivered{}, err
	}
	// The session endpoint's bearer rides in this file: nobody else reads it.
	return writeFile(fs, r.Build(), append(bytes, '\n'), 0o600)
}

// settingsFile writes the deny list and the statusline policy.
type settingsFile struct{ surface }

func (a *settingsFile) DeliverSettings(start present.Start, root present.RootKind, in engine.SettingsInputs, fs afero.Fs) (present.Delivered, error) {
	r, err := a.rooted(start, root, settingsRel)
	if err != nil {
		return present.Delivered{}, err
	}
	bytes, err := json.MarshalIndent(map[string]any{"denyTools": in.DenyTools, "statusline": in.Statusline}, "", "  ")
	if err != nil {
		return present.Delivered{}, err
	}
	return writeFile(fs, r.Build(), append(bytes, '\n'), 0o644)
}

// hooksFile writes the unified hook set as the mock's native hook file and
// announces it on --hooks; the mock's turn reads it back to fire them.
type hooksFile struct{ surface }

func (a *hooksFile) DeliverHooks(start present.Start, root present.RootKind, in engine.HooksInputs, fs afero.Fs) (present.Delivered, error) {
	r, err := a.rooted(start, root, hooksRel)
	if err != nil {
		return present.Delivered{}, err
	}
	bytes, err := json.MarshalIndent(in.Hooks, "", "  ")
	if err != nil {
		return present.Delivered{}, err
	}
	return writeFile(fs, r.AnnounceFlag(HooksFlag).Build(), append(bytes, '\n'), 0o644)
}

// commandsDir writes each enabled command as <name>.md under the commands
// dir, the body verbatim.
type commandsDir struct{ surface }

func (a *commandsDir) DeliverCommands(start present.Start, root present.RootKind, in engine.CommandsInputs, fs afero.Fs) (present.Delivered, error) {
	r, err := a.rooted(start, root, commandsRel)
	if err != nil {
		return present.Delivered{}, err
	}
	dir := r.Build()
	out := present.Delivered{Presented: dir}
	for _, c := range in.Commands {
		if !c.Enabled {
			continue
		}
		p := dir.Beneath(c.Name + ".md")
		d, err := writeFile(fs, p, c.Body, 0o644)
		if err != nil {
			return present.Delivered{}, err
		}
		out.Wrote = append(out.Wrote, d.Wrote...)
	}
	out.Undo = removeAll(out.Wrote)
	return out, nil
}

// skillsDir writes each enabled skill package under <skills>/<name>/, each
// file with its recorded mode so an exec bit survives.
type skillsDir struct{ surface }

func (a *skillsDir) DeliverSkills(start present.Start, root present.RootKind, in engine.SkillsInputs, fs afero.Fs) (present.Delivered, error) {
	r, err := a.rooted(start, root, skillsRel)
	if err != nil {
		return present.Delivered{}, err
	}
	dir := r.Build()
	out := present.Delivered{Presented: dir}
	for _, s := range in.Skills {
		if !s.Enabled {
			continue
		}
		for _, f := range s.Files {
			p := dir.Beneath(path.Join(s.Name, f.Path))
			mode := os.FileMode(f.Mode)
			if mode == 0 {
				mode = 0o644
			}
			d, err := writeFile(fs, p, f.Bytes, mode)
			if err != nil {
				return present.Delivered{}, err
			}
			out.Wrote = append(out.Wrote, d.Wrote...)
		}
	}
	out.Undo = removeAll(out.Wrote)
	return out, nil
}

// removeAll undoes a set of written files; nil when none was written.
func removeAll(paths []string) func(afero.Fs) error {
	if len(paths) == 0 {
		return nil
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	return func(fs afero.Fs) error {
		for _, p := range sorted {
			if err := fs.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
}
