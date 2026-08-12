package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// InstanceConfigFileName is claude's top-level config file, the one it reads
// from $CLAUDE_CONFIG_DIR when that variable is set. PROBE-VERIFIED (2026-08-12,
// claude 2.1.228, stable across 226/227/228): the relocated file sits DIRECTLY
// in the config dir as `<CLAUDE_CONFIG_DIR>/.claude.json`, not nested under a
// `.claude` leaf inside it.
const InstanceConfigFileName = ".claude.json"

// precedenceConfigFileName is the file claude prefers over
// InstanceConfigFileName when both exist in the config dir (probe-verified
// alongside it). ctxloom never writes one; its PRESENCE means the file we
// generate is inert, which is a silent no-op wearing a success's clothes — so
// it is checked for and reported rather than assumed away.
const precedenceConfigFileName = ".config.json"

// hostConfigRelPath is where the ambient half comes FROM: the user's own
// `~/.claude.json`, at the host home ROOT (claude keeps its unrelocated config
// beside ~/.claude, not inside it — see MCPConfigPath's host arm).
const hostConfigRelPath = InstanceConfigFileName

// ambientConfigKey is one entry of the `.claude.json` ALLOW-LIST — never a
// deny-list. A deny-list would copy each new key claude invents by default, and
// the default direction of a mistake there is a CONFIDENTIALITY LEAK: on a real
// host this file is claude's whole top-level config, carrying the user's own
// `mcpServers` registrations (and whatever secrets those hold), their
// `oauthAccount` identity, their accumulated per-project history and their
// telemetry keys. None of that is ever copied. Only the onboarding answers
// below cross, and they cross BY NAME.
type ambientConfigKey struct {
	// name is the exact JSON key.
	name string
	// fallback is written when the host file does not carry the key. nil means
	// "omit it" — claude writes its own on first run, and inventing a value for
	// a key whose semantics we only observed is worse than leaving it out.
	fallback any
	// expected marks a key whose ABSENCE from a real host file is schema drift
	// worth saying out loud: claude renamed or retired it, and this allow-list
	// is now copying nothing while still reporting success.
	expected bool
}

// ambientConfigKeys is claude's ambient set: the onboarding answers, and
// nothing else.
//
// WHY THESE AND NOTHING ELSE (D4, ruled 2026-08-11; key names probe-verified
// 2026-08-12 against claude 2.1.228 and the vendor's own headless fixture
// inside it): under `config_home: project` the instance is thrown away at
// session end, so an onboarding answer given inside one dies with it and the
// dialog re-prompts every interactive session. Copying the host file wholesale
// would fix that and re-open the leak above; copying these keys by name fixes
// it and cannot.
var ambientConfigKeys = []ambientConfigKey{
	{name: "hasCompletedOnboarding", fallback: true, expected: true},
	{name: "lastOnboardingVersion", expected: true},
	{name: "hasIdeOnboardingBeenShown"},
	{name: "hasClaudeMdExternalIncludesApproved"},
	{name: "hasClaudeMdExternalIncludesWarningShown"},
}

// hardenedConfigKeys are written UNCONDITIONALLY, host file or not — they are
// ctxloom's policy for a home it created, not the user's preference carried
// over:
//
//   - bypassPermissionsModeAccepted false: an instance must never inherit a
//     standing "yes, skip every permission prompt" answer. That answer belongs
//     to the human's own interactive session, and an agent run is not the human.
//   - autoUpdates false: a disposable instance that self-updates spends a
//     download and a version change on a home about to be deleted, and can
//     leave the run on a different binary than the one ctxloom probed.
var hardenedConfigKeys = map[string]any{
	"bypassPermissionsModeAccepted": false,
	"autoUpdates":                   false,
}

// projectTrustKeys is the per-project entry ctxloom GENERATES — it is not
// copied from the host and never reads the host's `projects` map at all, so
// there is no confidentiality question here to answer.
//
// This is a VENDOR-DOCUMENTED surface: claude's own error text instructs
// setting `projects.<dir>.hasTrustDialogAccepted` when a headless run meets an
// untrusted directory (probe-verified). It is the exact shape codex's
// addProjectTrust already has for `[projects."<abs>"] trust_level`, for the
// exact same reason: ctxloom answers the trust prompt only for homes it
// created and only for the directory the run was asked for. See
// docs/trust-model.md, "Engine workspace-trust prompts", for the normative
// statement of that boundary.
var projectTrustKeys = map[string]any{
	"hasTrustDialogAccepted":        true,
	"hasCompletedProjectOnboarding": true,
	"projectOnboardingSeenCount":    1,
}

// NewInstanceConfigWriter constructs claude's instance-config generator — the
// engine-owned writer of `<CLAUDE_CONFIG_DIR>/.claude.json` for a config home
// ctxloom provisioned. It is claude's half of the engine write-config
// directive: isolation.CopyAmbient decides that claude contributes a generated
// config at all, this decides what a single byte of it says.
func NewInstanceConfigWriter(o agent.SettingsOptions) agent.InstanceConfigWriter {
	return &claudeInstanceConfig{FS: o.FS}
}

// claudeInstanceConfig implements agent.InstanceConfigWriter for claude-code.
type claudeInstanceConfig struct {
	FS afero.Fs
}

var _ agent.InstanceConfigWriter = (*claudeInstanceConfig)(nil)

// WriteInstanceConfig generates `<InstanceHome>/claude/.claude.json` from three
// disjoint sources, in this order: the ambient allow-list copied out of the
// user's real `~/.claude.json`, ctxloom's hardened policy keys, and the
// generated project-trust entry for WorkDir.
//
// An EXISTING instance file is the base, not a casualty: two runs share one
// session instance (a coordinator and its in-tree delegated child), and claude
// itself writes into this file while it runs. Loading it first means a second
// run refreshes the three classes above and preserves everything claude
// accumulated, instead of resetting the instance under a live engine. The
// caller holds the project filelock across this, so the load-modify-write
// cannot interleave with the other run's.
func (w *claudeInstanceConfig) WriteInstanceConfig(req agent.InstanceConfigRequest) (agent.InstanceConfigReport, error) {
	var rep agent.InstanceConfigReport
	if req.InstanceHome == "" {
		return rep, fmt.Errorf("claude instance config: no instance home to generate %s in", InstanceConfigFileName)
	}
	fs := agent.GetFS(w.FS)
	dir := filepath.Join(req.InstanceHome, inTreeConfigLeaf)
	dest := filepath.Join(dir, InstanceConfigFileName)

	cfg, err := loadJSONObject(fs, dest)
	if err != nil {
		// The instance file is ctxloom's own; one we cannot read is a real
		// fault, not a reason to replace the running engine's state with a
		// fresh table.
		return rep, fmt.Errorf("claude instance config: cannot read %s: %w", dest, err)
	}

	rep.Warnings = append(rep.Warnings, w.applyAmbient(fs, req.HostHome, cfg)...)
	for name, value := range hardenedConfigKeys {
		cfg[name] = value
	}
	if warn := applyProjectTrust(cfg, req.WorkDir); warn != "" {
		rep.Warnings = append(rep.Warnings, warn)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return rep, fmt.Errorf("claude instance config: encode %s: %w", dest, err)
	}
	data = append(data, '\n')
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return rep, fmt.Errorf("claude instance config: create %s: %w", dir, err)
	}
	if err := agent.AtomicWriteFile(fs, dest, data, InstanceConfigFileName); err != nil {
		return rep, fmt.Errorf("claude instance config: write %s: %w", dest, err)
	}
	rep.Wrote = append(rep.Wrote, dest)

	if shadow, _ := afero.Exists(fs, filepath.Join(dir, precedenceConfigFileName)); shadow {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"a %s sits beside %s in %s and claude reads it in preference; the onboarding and trust answers just written there will not take effect",
			precedenceConfigFileName, InstanceConfigFileName, dir))
	}
	return rep, nil
}

// applyAmbient copies the allow-listed keys out of the user's real
// ~/.claude.json into cfg, and returns the schema-drift warnings.
//
// An unreadable or unparseable host file is a WARNING, never an error: it is
// the user's file, ctxloom does not own it, and the instance is still perfectly
// usable with the fallbacks — refusing to launch over it would trade a working
// run for a fixable annoyance.
func (w *claudeInstanceConfig) applyAmbient(fs afero.Fs, hostHome string, cfg map[string]any) []string {
	var warnings []string
	var host map[string]any

	switch {
	case hostHome == "":
		warnings = append(warnings, fmt.Sprintf(
			"the host home could not be resolved, so no %s onboarding answers were carried into this run; claude may re-run its onboarding", InstanceConfigFileName))
	default:
		hostPath := filepath.Join(hostHome, hostConfigRelPath)
		loaded, err := loadJSONObject(fs, hostPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"cannot read the host %s (%v), so no onboarding answers were carried into this run; claude may re-run its onboarding", hostPath, err))
		} else {
			host = loaded
		}
	}

	for _, key := range ambientConfigKeys {
		if value, ok := host[key.name]; ok {
			cfg[key.name] = value
			continue
		}
		if key.expected && host != nil {
			warnings = append(warnings, fmt.Sprintf(
				"the host %s carries no %q; claude's config schema may have changed and this run's onboarding state is being guessed",
				InstanceConfigFileName, key.name))
		}
		if key.fallback != nil {
			cfg[key.name] = key.fallback
		}
	}
	return warnings
}

// applyProjectTrust writes the generated trust entry for workDir into cfg's
// `projects` map, preserving every other project entry already in the instance
// file (claude's own accumulated per-project state) and every other key of THIS
// project's entry.
//
// The key is the ABSOLUTE, cleaned workDir — the directory the run actually
// works in — and deliberately NOT the instance path: trusting the config home
// would answer a question claude never asks and leave the real workspace
// untrusted, which headless means proceeding without tools rather than
// prompting.
func applyProjectTrust(cfg map[string]any, workDir string) string {
	if workDir == "" {
		return "this run named no working directory, so no workspace-trust answer was generated; claude may refuse tools or re-prompt"
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Sprintf("cannot canonicalize the working directory %q (%v), so no workspace-trust answer was generated", workDir, err)
	}
	abs = filepath.Clean(abs)

	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	entry, _ := projects[abs].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	for name, value := range projectTrustKeys {
		entry[name] = value
	}
	projects[abs] = entry
	cfg["projects"] = projects
	return ""
}

// loadJSONObject reads a JSON object from path. An ABSENT file is an empty
// object (nothing to preserve); an unparseable one is an error, because every
// caller writes the returned table straight back and a degraded-to-empty parse
// would destroy whatever it could not read.
func loadJSONObject(fs afero.Fs, path string) (map[string]any, error) {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	obj := map[string]any{}
	if len(data) == 0 {
		return obj, nil
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", path, err)
	}
	return obj, nil
}
