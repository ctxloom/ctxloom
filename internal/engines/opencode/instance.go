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
// model_options), the ctxloom provider's model, the session to continue,
// and --auto, which approves what the posture leaves to ask.
const (
	cmdRun       = "run"
	flagFormat   = "--format"
	flagThinking = "--thinking"
	flagModel    = "--model"
	flagSession  = "--session"
	flagAuto     = "--auto"
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
}

// Exec composes the process: the binary (the session's, else the label's
// binary_path, else opencode), the label's args, `run --format json
// --thinking`, the model, the resumed session, --auto at the auto posture;
// the prompt on stdin. Env holds only opencode's own variables: the home
// vars, every presentation's env, the composed config, the posture and the
// isolation switches. The key is never in it: the run's credentials carry
// it, and the config names it by reference.
func (i *instance) Exec(presented []present.Presentation) (engine.Exec, error) {
	args := slices.Clone(i.s.Label.Args)
	if len(args) == 0 {
		args = slices.Clone(i.cfg.Args)
	}
	args = append(args, cmdRun, flagFormat, "json", flagThinking)
	if m := i.s.Label.Model; m != "" {
		args = append(args, flagModel, providerID+"/"+m)
	}
	if i.key != "" {
		args = append(args, flagSession, i.key)
	}
	if i.pos.mode == modeAuto {
		args = append(args, flagAuto)
	}
	env, err := i.execEnv(presented)
	if err != nil {
		return engine.Exec{}, err
	}
	ex := engine.Exec{Binary: i.binary(), Args: args, Env: env, WorkDir: i.s.WorkDir}
	if i.s.Prompt != "" {
		ex.StdinPrompt = []byte(i.s.Prompt)
	}
	return ex, nil
}

func (i *instance) binary() string {
	switch {
	case i.s.Label.Binary != "":
		return i.s.Label.Binary
	case i.cfg.BinaryPath != "":
		return i.cfg.BinaryPath
	}
	return defaultBinary
}

// execEnv is the engine-native env (kit.ComposeEnv's home vars and
// presentations first).
func (i *instance) execEnv(presented []present.Presentation) (map[string]string, error) {
	env := kit.ComposeEnv(i.s, presented)
	untrusted := i.s.Trust != engine.TrustTrusted
	content, err := configContent(i.s, i.cfg, untrusted && i.projectAgentsExists())
	if err != nil {
		return nil, err
	}
	perm, err := i.pos.document("")
	if err != nil {
		return nil, err
	}
	env[envConfigContent] = content
	env[envPermission] = perm
	for _, v := range []string{envDisableClaudeCode, envDisableAutoupdate, envDisableModelsFetch, envDisableExternalSkills} {
		env[v] = "1"
	}
	if untrusted {
		env[envDisableProjectConfig] = "1"
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
	if i.pos.turnMode(in.Posture) == modeAuto {
		args = append(args, flagAuto)
	}
	return args, nil
}

// turnEnv is the turn's posture as OPENCODE_PERMISSION, over the session's.
func (i *instance) turnEnv(_ engine.Exec, in engine.Turn) (map[string]string, error) {
	doc, err := i.pos.document(in.Posture.Mode)
	if err != nil {
		return nil, err
	}
	return map[string]string{envPermission: doc}, nil
}

var _ engine.Instance = (*instance)(nil)
