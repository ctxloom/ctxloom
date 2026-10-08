package mock

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The mock's native surfaces: every kind is a FILE the mock reads, under
// whichever root the plan selected — the context file at the root, the rest
// under its config dir. A test asserts what was delivered by reading the
// file, without an engine binary; the mock's own turn reads the hooks file
// to fire hooks (hooks.go).

// ContextFileName is the mock's well-known context file — its analogue of
// CLAUDE.md / AGENTS.md. It lives at the ROOT (not nested) so it is named
// where a human would look for it.
const ContextFileName = "MOCK_CONTEXT.md"

// The remaining surfaces, relative to the root, all under ConfigDirName: the
// shape a real engine has (.claude/), not a top-level scatter. skillsRel is
// NESTED for the same reason, and because a bare top-level `skills/` would
// collide with the `skills/` directory of a bundle content tree materialized
// into the same project.
const (
	mcpRel      = ConfigDirName + "/mcp.json"
	settingsRel = ConfigDirName + "/settings.json"
	hooksRel    = ConfigDirName + "/hooks.json"
	commandsRel = ConfigDirName + "/commands"
	skillsRel   = ConfigDirName + "/skills"
)

// The argv flags the mock's presentations announce their files on, one per
// surface; the mock's CLI grammar declares each. Every surface is announced,
// not only the two the mock reads to act (context, hooks), so a turn can
// record where each one was delivered (recordTurn). HooksFlag is exported for
// the mock binary's interactive loop (mock/runtime), which reads the
// delivered hook file off its own argv to fire turn_start per typed line.
const (
	contextFlag  = "--context"
	mcpFlag      = "--mcp"
	settingsFlag = "--settings"
	HooksFlag    = "--hooks"
	commandsFlag = "--commands"
	skillsFlag   = "--skills"
)

// surface is the shared half of every mock approach: its name and traits
// (kit.Approach), with rel the same file under either root. It is not
// Private: a double's session-home form is served from whatever session home
// the start names, rooted or not.
type surface struct{ kit.Approach }

func newSurface(name string, t present.Traits) surface {
	return surface{kit.Approach{Engine: Name, ApproachName: name, T: t}}
}

// rooted composes rel under the root the plan selected; the approach
// offers exactly the roots its traits list, so any other is a refusal.
func (s *surface) rooted(start present.Start, root present.RootKind, rel string) (present.Rooted, error) {
	return s.Rooted(start, root, rel, rel)
}

// writeFile writes bytes at the composed presentation's host path and
// returns that path, for the approach to declare.
func writeFile(fs afero.Fs, p present.Presentation, bytes []byte, mode os.FileMode) (string, error) {
	if err := fs.MkdirAll(filepath.Dir(p.HostPath), 0o755); err != nil {
		return "", err
	}
	if err := safefs.WriteFile(fs, p.HostPath, bytes, mode); err != nil {
		return "", err
	}
	return p.HostPath, nil
}

// writeWhole writes p's file and declares it: the delivery of a kind that is
// one file the mock owns whole.
// deliverJSON writes v, indented, as the whole file at rel under the root the
// plan selected, announcing it on flag: the shape every JSON surface of the
// mock shares.
func (s *surface) deliverJSON(start present.Start, root present.RootKind, rel, flag string, v any, mode os.FileMode, fs afero.Fs) (present.Delivered, error) {
	r, err := s.rooted(start, root, rel)
	if err != nil {
		return present.Delivered{}, err
	}
	bytes, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return present.Delivered{}, err
	}
	return writeWhole(fs, r.AnnounceFlag(flag).Build(), append(bytes, '\n'), mode)
}

func writeWhole(fs afero.Fs, p present.Presentation, bytes []byte, mode os.FileMode) (present.Delivered, error) {
	path, err := writeFile(fs, p, bytes, mode)
	if err != nil {
		return present.Delivered{}, err
	}
	return present.Delivered{Presented: p, Files: []string{path}}, nil
}

// contextFile claims the assembled context as a section of MOCK_CONTEXT.md —
// after whatever a human already wrote there, which stays theirs — and
// announces the file on --context.
type contextFile struct{ surface }

func (a *contextFile) DeliverContext(start present.Start, root present.RootKind, in engine.ContextInputs, _ afero.Fs) (present.Delivered, error) {
	name := ContextFileName
	if in.File != "" {
		if root != present.RootProjectRoot {
			return present.Delivered{}, fmt.Errorf("%w: mock/%s at %v", engine.ErrContextFileUnsupported, a.Name(), root)
		}
		name = in.File
	}
	r, err := a.rooted(start, root, name)
	if err != nil {
		return present.Delivered{}, err
	}
	return kit.AppendedSection(r.AnnounceFlag(contextFlag).Build(), in.Text), nil
}

// mcpFile writes the server set as {"mcpServers": {...}}.
type mcpFile struct{ surface }

func (a *mcpFile) DeliverMCP(start present.Start, root present.RootKind, in engine.MCPInputs, fs afero.Fs) (present.Delivered, error) {
	// Owner-only: the session endpoint's bearer rides in this file.
	return a.deliverJSON(start, root, mcpRel, mcpFlag, map[string]any{"mcpServers": in.Servers}, 0o600, fs)
}

// settingsFile writes the settings inputs it is handed: the deny list, the
// statusline policy and the shell timeout in milliseconds. The mock's turn
// runs no shell tool, so the timeout is recorded, not applied.
type settingsFile struct{ surface }

func (a *settingsFile) DeliverSettings(start present.Start, root present.RootKind, in engine.SettingsInputs, fs afero.Fs) (present.Delivered, error) {
	shell := map[string]int64{"defaultMs": in.ShellTimeout.Default.Milliseconds(), "maxMs": in.ShellTimeout.Max.Milliseconds()}
	return a.deliverJSON(start, root, settingsRel, settingsFlag, map[string]any{"denyTools": in.DenyTools, "statusline": in.Statusline, "shellTimeout": shell}, 0o644, fs)
}

// hooksFile writes the unified hook set, bound to this engine
// (agent.BindHooks: ctxloom's callbacks name it, tool classes become its
// matchers), as the mock's native hook file and announces it on --hooks; the
// mock's turn reads it back to fire them. An event the kind declares lost
// (Definition.HookLosses) is left out of the file: the mock fires no hook for
// it, so a file that still carried one would disagree with the loss report.
type hooksFile struct {
	surface
	engine engine.Name
	lost   map[string]string
}

func (a *hooksFile) DeliverHooks(start present.Start, root present.RootKind, in engine.HooksInputs, fs afero.Fs) (present.Delivered, error) {
	bound, err := agent.BindHooks(in.Hooks, string(a.engine), toolMatcher)
	if err != nil {
		return present.Delivered{}, err
	}
	for event := range a.lost {
		bound.SetEvent(event, nil)
	}
	return a.deliverJSON(start, root, hooksRel, HooksFlag, bound, 0o644, fs)
}

// commandsDir writes each enabled command as <name>.md under the commands
// dir, the body verbatim, through the shared managed writer (a traversal or
// absolute name is skipped with a warning).
type commandsDir struct{ surface }

func (a *commandsDir) DeliverCommands(start present.Start, root present.RootKind, in engine.CommandsInputs, files safefs.Root) (present.Delivered, error) {
	return kit.DeliverCommands(a.Approach, start, root, commandsRel, commandsRel, files, in, verbatimCommand, announce(commandsFlag))
}

// verbatimCommand is the mock's command file: <name>.md, the body as given.
func verbatimCommand(c agent.CommandExport) (string, []byte, error) {
	return c.Name + ".md", []byte(c.Content), nil
}

// skillsDir writes each enabled skill package under <skills>/<name>/, each
// file with its recorded mode so an exec bit survives, through the shared
// managed writer. The mock loads every skill it is handed: no constraints.
type skillsDir struct{ surface }

func (a *skillsDir) DeliverSkills(start present.Start, root present.RootKind, in engine.SkillsInputs, files safefs.Root) (present.Delivered, error) {
	return kit.DeliverSkills(a.Approach, start, root, skillsRel, skillsRel, files, in, nil, announce(skillsFlag))
}

// announce names a managed tree's directory on flag.
func announce(flag string) func(present.Rooted) present.Rooted {
	return func(r present.Rooted) present.Rooted { return r.AnnounceFlag(flag) }
}
