package opencode

import (
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

// This file is the INSTANCE half: one kind bound to one session. Exec is the
// only place opencode's argv and env are composed; the structured driver
// (kit.ProcessTurn) runs each turn over it, adding the turn's resume key and
// posture.

// defaultBinary is the CLI a label that names none runs.
const defaultBinary = "opencode"

// The structured protocol's argv: `run` with JSON events, reasoning emitted
// (it only gates emission; whether the model thinks is the label's
// model_options), the model (modelRef), the session to continue,
// and --auto, which approves what the posture leaves to ask.
const (
	cmdRun       = "run"
	flagFormat   = "--format"
	flagThinking = "--thinking"
	flagModel    = "--model"
	flagSession  = "--session"
	flagAuto     = "--auto"
	// The TUI's own: the newest session of the session home's private
	// store, and the first prompt, submitted once the model is ready.
	flagContinue = "--continue"
	flagPrompt   = "--prompt"
)

// structuredGrammar is the argv grammar Exec's output parses against (the
// conformance suite's anti-drift test). The label's own args are opaque
// passthrough and undeclarable by construction.
var structuredGrammar = engine.CLIGrammar{
	Mode:   engine.Structured,
	Binary: defaultBinary,
	Flags: []engine.Flag{
		{Name: flagFormat, HasValue: true},
		{Name: flagThinking},
		{Name: flagModel, HasValue: true},
		{Name: flagSession, HasValue: true},
		{Name: flagAuto},
	},
	Positional: 1, // the run subcommand; the prompt rides stdin
}

// interactiveGrammar is the TUI's: bare `opencode` on ctxloom's pty, the
// working directory its project (no positional), the prompt a flag value.
var interactiveGrammar = engine.CLIGrammar{
	Mode:   engine.Interactive,
	Binary: defaultBinary,
	Flags: []engine.Flag{
		{Name: flagModel, HasValue: true},
		{Name: flagSession, HasValue: true},
		{Name: flagContinue},
		{Name: flagPrompt, HasValue: true},
		{Name: flagAuto},
	},
}

// The env opencode reads beyond its home vars: ctxloom's composed config,
// the posture (applied after every config source), and the switches that
// keep it off the network and off the user's and the repository's files.
const (
	envConfigContent         = "OPENCODE_CONFIG_CONTENT"
	envPermission            = "OPENCODE_PERMISSION"
	envDisableProjectConfig  = "OPENCODE_DISABLE_PROJECT_CONFIG"
	envDisableClaudeCode     = "OPENCODE_DISABLE_CLAUDE_CODE"
	envDisableAutoupdate     = "OPENCODE_DISABLE_AUTOUPDATE"
	envDisableModelsFetch    = "OPENCODE_DISABLE_MODELS_FETCH"
	envDisableExternalSkills = "OPENCODE_DISABLE_EXTERNAL_SKILLS"
	// envPWD is set to the session's working directory: `opencode run`
	// takes its project directory from $PWD rather than the process cwd, so
	// the launching shell's PWD would otherwise win over Exec.WorkDir.
	envPWD = "PWD"
)

// Instance binds one session: the label's body decoded, the posture read.
func (o Opencode) Instance(s engine.Session) (engine.Instance, error) {
	if o.Context == nil {
		return nil, engine.ErrUnsupported{Engine: o.Name, Capability: "context"}
	}
	cfg, err := decodeConfig(s.Label.Body)
	if err != nil {
		return nil, err
	}
	pos, err := postureOf(s.Permission)
	if err != nil {
		return nil, err
	}
	return &instance{o: o, s: s, cfg: cfg, pos: pos}, nil
}

// instance is one kind bound to one session; key is the native session to
// continue, set by Resume.
type instance struct {
	o   Opencode
	s   engine.Session
	cfg Config
	pos posture
	key string
	// totals are the session's running output and cost across its turns.
	totals sessionTotals
	// toolLess is opencode's tool-less launch (ToolLess): every tool
	// denied, every turn, and the project's own config off.
	toolLess bool
}

// Exec composes the process: the label's binary (the launch carries the
// label's binary_path as LabelConfig.Binary), else opencode, the label's args
// (LabelConfig.Args), then — for a structured session — `run --format json
// --thinking` with the prompt on stdin, or — for an interactive one — the
// bare TUI on a pty with the prompt as --prompt; the model, the resumed
// session (resumeArgs), and --auto at the auto posture. Env, identical in
// both modes, holds only opencode's own variables: the home vars, every
// presentation's env, the composed config, the posture, the isolation
// switches and PWD (the working directory; opencode reads it, not the cwd). The key is never in it: the run's credentials carry it, and the
// config names it by reference.
func (i *instance) Exec(presented []present.Presentation) (engine.Exec, error) {
	interactive := i.s.Mode == engine.Interactive
	args := slices.Clone(i.s.Label.Args)
	if !interactive {
		args = append(args, cmdRun, flagFormat, "json", flagThinking)
	}
	if m := modelRef(i.s.Label.Model, i.cfg); m != "" {
		args = append(args, flagModel, m)
	}
	args = append(args, i.resumeArgs()...)
	if interactive && i.s.Prompt != "" {
		// Inline, so a prompt that starts with "-" stays the flag's value.
		args = append(args, flagPrompt+"="+i.s.Prompt)
	}
	if i.pos.mode == modeAuto {
		args = append(args, flagAuto)
	}
	env, err := i.execEnv(presented)
	if err != nil {
		return engine.Exec{}, err
	}
	ex := engine.Exec{Binary: i.binary(), Args: args, Env: env, WorkDir: i.s.WorkDir, Interactive: interactive}
	if !interactive && i.s.Prompt != "" {
		ex.StdinPrompt = []byte(i.s.Prompt)
	}
	return ex, nil
}

// resumeArgs names the native session the Exec continues: the key Resume
// set; for the TUI, else the key the launch resumes; else, for the TUI
// relaunching a ctxloom session (the same session home, whose private store
// holds only this session's conversations), its newest one (--continue).
// A structured turn's key arrives per turn instead (turnArgv).
func (i *instance) resumeArgs() []string {
	key := i.key
	interactive := i.s.Mode == engine.Interactive
	if key == "" && interactive {
		key = i.s.Resume.NativeKey
	}
	switch {
	case key != "":
		return []string{flagSession, key}
	case interactive && i.s.Resume.Harp != "":
		return []string{flagContinue}
	}
	return nil
}

func (i *instance) binary() string {
	if i.s.Label.Binary != "" {
		return i.s.Label.Binary
	}
	return defaultBinary
}

// execEnv is the engine-native env (kit.ComposeEnv's home vars and
// presentations first).
func (i *instance) execEnv(presented []present.Presentation) (map[string]string, error) {
	env := kit.ComposeEnv(i.s, presented)
	// A tool-less launch keeps the project's own config out on a trusted
	// repository too: a rule of its would follow the deny-all.
	noProjectConfig := i.s.Trust != engine.TrustTrusted || i.toolLess
	content, err := configContent(i.s, i.cfg, noProjectConfig && i.projectAgentsExists())
	if err != nil {
		return nil, err
	}
	perm, err := i.permission("")
	if err != nil {
		return nil, err
	}
	env[envConfigContent] = content
	env[envPermission] = perm
	if i.s.WorkDir != "" {
		env[envPWD] = i.s.WorkDir
	}
	for _, v := range []string{envDisableClaudeCode, envDisableAutoupdate, envDisableModelsFetch, envDisableExternalSkills} {
		env[v] = "1"
	}
	if noProjectConfig {
		env[envDisableProjectConfig] = "1"
	}
	path, err := wake.Ensure(i.s)
	if err != nil {
		return nil, err
	}
	if path != "" {
		env[wake.EnvVar] = path
	}
	return env, nil
}

// projectAgentsExists reports whether the project root holds an AGENTS.md
// (read on the host side; opencode is told its engine path).
func (i *instance) projectAgentsExists() bool {
	host := i.s.Roots.ProjectRoot.Host
	if host == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(host, ContextFileName))
	return err == nil && fi.Mode().IsRegular()
}

// Drivers: the JSON event stream of `opencode run` is the one structured
// driver, a discrete process per turn (kit.ProcessTurn). opencode supplies
// its per-turn argv, its posture env, the raw prompt on stdin and its line
// codec.
func (i *instance) Drivers() []engine.StructuredDriver {
	return []engine.StructuredDriver{kit.ProcessTurn{
		Name:        string(EngineName),
		Argv:        i.turnArgv,
		Env:         i.turnEnv,
		WritePrompt: func(w io.Writer, prompt string) error { _, err := io.WriteString(w, prompt); return err },
		NewMapper:   func() kit.LineMapper { return newTurnStream(i.cfg.ContextWindow, &i.totals, i.o.now) },
		Open:        i.o.open,
		Now:         i.o.now,
	}}
}

// Resume re-attaches the instance to a native session: the next Exec
// continues it.
func (i *instance) Resume(key string) error {
	i.key = key
	return nil
}

// turnArgv is the Exec's argv for one turn: the native key the turn names
// (unless the Exec already resumes one), and --auto exactly when the turn's
// posture is auto (a turn may move off or onto it).
func (i *instance) turnArgv(ex engine.Exec, in engine.Turn) ([]string, error) {
	args := slices.DeleteFunc(slices.Clone(ex.Args), func(a string) bool { return a == flagAuto })
	if in.Resume != "" && !slices.Contains(args, flagSession) {
		args = append(args, flagSession, in.Resume)
	}
	if !i.toolLess && i.pos.turnMode(in.Posture) == modeAuto {
		args = append(args, flagAuto)
	}
	return args, nil
}

// turnEnv is the turn's posture as OPENCODE_PERMISSION, over the session's.
func (i *instance) turnEnv(_ engine.Exec, in engine.Turn) (map[string]string, error) {
	doc, err := i.permission(in.Posture.Mode)
	if err != nil {
		return nil, err
	}
	return map[string]string{envPermission: doc}, nil
}

var _ engine.Instance = (*instance)(nil)
