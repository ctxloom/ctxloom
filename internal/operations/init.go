package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/resources"
)

// InitializeProjectRequest is the input for InitializeProject.
type InitializeProjectRequest struct {
	AppDir string `json:"app_dir"`
	Engine string `json:"engine"`

	// DirtyTreeHandler and DirtyTreeCommitAck carry the init interview's
	// single dirty-tree-handler answer (internal/cli/init.go's
	// promptDirtyTreeHandler) through. Both empty/false (the zero values)
	// reproduce today's behavior exactly: an unset project default resolving
	// to the built-in "commit" default, unacknowledged, so the commit handler
	// still refuses a delegated spawn until a human explicitly acknowledges
	// it. DirtyTreeHandler is written into config.yaml (BuildInitialConfig);
	// DirtyTreeCommitAck is NOT — it is written to
	// paths.DirtyTreeCommitAckPath via config.SetDirtyTreeCommitAck, an
	// admission.Store file outside the layered config chain entirely (see
	// that function's doc for why: a config key is reachable from three
	// channels an agent can write, and prior human consent needs a home with
	// none).
	DirtyTreeHandler   string `json:"dirty_tree_handler"`
	DirtyTreeCommitAck bool   `json:"dirty_tree_commit_ack"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// InitializeProjectResult reports the bootstrap.
type InitializeProjectResult struct {
	Status string `json:"status"`
	AppDir string `json:"app_dir"`
}

// SeedProfileName is the file/profile name of the LOCAL default coding profile
// `init` scaffolds; init also binds it as the profile of the always-bound
// DEFAULT AGENT (config `agents.default` + `default_agent`), which a bare
// `ctxloom run` resolves. Generic by design (not any role/archetype): the
// archetype taxonomy — developer/finder/code-review, per-(language × lens)
// members — is data, living in the agent-setup prompt and ctxloom-default's
// profiles, never baked into this binary as names.
const SeedProfileName = "default"

// InitializeProject creates the .ctxloom skeleton (dir tree + config.yaml
// carrying the chosen engine + default remotes.yaml + a scaffolded local
// default coding profile). Safe to re-run: directories use MkdirAll and the
// scaffold files are overwritten — EXCEPT the seed profile, which is left
// untouched if it already exists so a re-init never clobbers user edits.
//
// req.Engine is validated against the backends registry (internal/lm/backends
// — the one place a "known engine" is defined) BEFORE anything is written: an
// unknown value refuses loud, with the value named and the valid set listed,
// rather than scaffolding a config.yaml that then fails ctxloom's own
// JSON-schema validation on the very next command (the silent-no-op-shaped
// failure this codebase treats as a bug, not a shortcut). Every caller that
// accepts a user-typed --engine (manage install, config create and its
// deprecated aliases, root init) funnels through here, so this is the single
// choke point — no per-call-site duplicate check needed.
func InitializeProject(_ context.Context, req InitializeProjectRequest) (*InitializeProjectResult, error) {
	if req.AppDir == "" {
		return nil, fmt.Errorf("app dir is required")
	}
	if !backends.Exists(req.Engine) {
		return nil, fmt.Errorf("unknown engine %q; valid engines: %s", req.Engine, strings.Join(backends.List(), ", "))
	}
	fs := getFS(req.FS)
	// The authored-bundles home is the COMMITTED content tree; the cache is
	// created lazily by whatever fetches into it, and init has no business
	// scaffolding a gitignored directory.
	for _, dir := range []string{req.AppDir, filepath.Join(req.AppDir, paths.ProfilesDir), paths.LocalBundlesPath(req.AppDir)} {
		if err := fs.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	configData, err := BuildInitialConfig(req.Engine, req.DirtyTreeHandler)
	if err != nil {
		return nil, fmt.Errorf("failed to build config.yaml: %w", err)
	}
	if err := afero.WriteFile(fs, paths.ConfigPath(req.AppDir), configData, 0644); err != nil {
		return nil, fmt.Errorf("failed to create config.yaml: %w", err)
	}

	// The dirty-tree-commit acknowledgement is written OUTSIDE config.yaml
	// (paths.DirtyTreeCommitAckPath) — see InitializeProjectRequest's doc.
	// Only write it when granted: absent is exactly the same "not yet
	// acknowledged" state as an explicit false record, and skipping the write
	// keeps a checkout that never touched this question from growing a state
	// file for no reason.
	if req.DirtyTreeCommitAck {
		if err := config.SetDirtyTreeCommitAck(fs, req.AppDir, true); err != nil {
			return nil, fmt.Errorf("failed to record dirty-tree-commit acknowledgement: %w", err)
		}
	}

	remotesContent, err := resources.GetDefaultRemotes()
	if err != nil {
		return nil, fmt.Errorf("failed to read default remotes: %w", err)
	}
	if err := afero.WriteFile(fs, paths.RemotesPath(req.AppDir), remotesContent, 0644); err != nil {
		return nil, fmt.Errorf("failed to create remotes.yaml: %w", err)
	}

	if err := scaffoldSeedProfile(fs, req.AppDir); err != nil {
		// Fault tolerance (CLAUDE.md): a missing/unwritable seed profile must not
		// fail init. The config still names `default` as the wired default, and
		// the user can author it. Warn so the gap is diagnosable.
		clidiag.Warn("ctxloom", "failed to scaffold default profile: %v", err)
	}

	return &InitializeProjectResult{Status: "initialized", AppDir: req.AppDir}, nil
}

// scaffoldSeedProfile writes the embedded local default coding profile into
// .ctxloom/profiles/<SeedProfileName>.yaml. It is write-if-absent: a profile is
// user-editable content, so a re-init must not overwrite a default the user has
// since customized (unlike config.yaml/remotes.yaml, which are scaffolding).
func scaffoldSeedProfile(fs afero.Fs, appDir string) error {
	dest := filepath.Join(paths.ProfilesPath(appDir), SeedProfileName+".yaml")
	if exists, _ := afero.Exists(fs, dest); exists {
		return nil
	}
	data, err := resources.GetSeedProfile(SeedProfileName)
	if err != nil {
		return fmt.Errorf("read embedded seed profile: %w", err)
	}
	if err := afero.WriteFile(fs, dest, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

// BuildInitialConfig renders the config.yaml a freshly initialized project
// starts from. It loads the embedded init scaffold (version + settings + mcp,
// no llm block), wires the selected engine's LLM registry into it, and marshals
// the result. The registry is built from the shipped default-config: the two
// entries whose type matches the engine and whose role is primary/fast are
// copied in (role stripped) and pointed at by llm.defaults. Engines without
// role-marked entries (e.g. codex, mock) get a single self-contained
// {type: engine} entry serving both roles.
//
// dirtyTreeHandler carries the init interview's dirty-tree-handler answer
// straight into the scaffolded config (config.Fixture.DirtyTreeHandler) —
// empty reproduces the pre-interview shape (no key written, built-in "commit"
// default). The interview's OTHER half, the commit acknowledgement, is never
// part of this scaffold at all — see InitializeProject, which writes it to
// paths.DirtyTreeCommitAckPath instead.
func BuildInitialConfig(engine, dirtyTreeHandler string) ([]byte, error) {
	scaffoldData, err := readResource(resources.GetInitConfig, "init scaffold")
	if err != nil {
		return nil, err
	}
	scaffold, err := config.ParseConfig(scaffoldData)
	if err != nil {
		return nil, fmt.Errorf("parse init scaffold: %w", err)
	}

	registryData, err := readResource(resources.GetDefaultConfig, "default registry")
	if err != nil {
		return nil, err
	}
	registry, err := config.ParseConfig(registryData)
	if err != nil {
		return nil, fmt.Errorf("parse default registry: %w", err)
	}

	f := scaffold.ToFixture()
	f.LM = engineRegistry(engine, registry.GetLMConfig())
	f.DirtyTreeHandler = dirtyTreeHandler
	// PrimaryLabel reads f.LM.Defaults.Primary; resolve it through a throwaway
	// Config carrying just that update so the seed agent's Engine matches
	// exactly what scaffold.PrimaryLabel() would have returned post-mutation.
	primaryLabel := config.NewFixture(config.Fixture{LM: f.LM}).PrimaryLabel()

	// Bind the always-bound DEFAULT AGENT to the scaffolded seed profile, carrying
	// the selected engine's primary label so a bare `ctxloom run` launches the same
	// backend. This replaces the retired profiles.defaults: the default context is
	// now whatever the default agent composes (Config.DefaultAgentProfiles).
	f.DefaultAgent = SeedProfileName
	f.Agents = map[string]agents.Agent{
		SeedProfileName: {
			Engine:   primaryLabel,
			Runtime:  "host",
			Profiles: []string{SeedProfileName},
		},
	}
	return config.NewFixture(f).Marshal()
}

// engineRegistry builds the llm block for an engine by selecting its
// role-marked entries out of the shipped registry. When the engine has both a
// primary and a fast entry, both are copied (role cleared) under their original
// labels with defaults pointing at them. Otherwise it falls back to a single
// {type: engine} entry that plays both roles.
func engineRegistry(engine string, registry config.LMConfig) config.LMConfig {
	primaryLabel := roleLabel(registry, engine, "primary")
	fastLabel := roleLabel(registry, engine, "fast")

	if primaryLabel == "" {
		return fallbackRegistry(engine)
	}
	if fastLabel == "" {
		fastLabel = primaryLabel
	}

	out := config.LMConfig{
		Configs:  map[string]config.LLMConfig{},
		Defaults: config.RoleDefaults{Primary: primaryLabel, Fast: fastLabel},
	}
	for _, label := range []string{primaryLabel, fastLabel} {
		entry := registry.Configs[label]
		entry.Role = "" // role is registry-only; user configs carry plain entries
		out.Configs[label] = entry
	}
	return out
}

// fallbackRegistry produces a self-contained single-entry registry for an
// engine the shipped registry does not mark with roles. Both roles point at the
// lone {type: engine} entry so resolution still succeeds.
func fallbackRegistry(engine string) config.LMConfig {
	return config.LMConfig{
		Configs:  map[string]config.LLMConfig{engine: {Type: engine}},
		Defaults: config.RoleDefaults{Primary: engine, Fast: engine},
	}
}

// roleLabel returns the registry label whose entry has the given backend type
// and role, or "" when none matches.
func roleLabel(registry config.LMConfig, engine, role string) string {
	for label, entry := range registry.Configs {
		if entry.Type == engine && entry.Role == role {
			return label
		}
	}
	return ""
}

// readResource reads an embedded resource, naming it in any failure.
//
// This replaces `mustResource`, which was named for a contract it
// did not implement — it discarded the error and returned nil bytes. Nothing
// downstream caught that: config.ParseConfig(nil) is yaml.Unmarshal of nil,
// which succeeds and yields an EMPTY config, so a build with a broken embed
// scaffolded a project whose config.yaml had no settings, no mcp block and no
// llm registry, silently, at exit 0. `must*` in Go means "panic on failure";
// this one swallowed instead, which is the opposite.
//
// The failure is a build/embed fault rather than anything a user can cause
// (internal/config/arch_test.go asserts every accessor resolves), so the point
// is not that it happens often — it is that when it does, init must not write
// a hollow config and call it a project.
func readResource(read func() ([]byte, error), what string) ([]byte, error) {
	data, err := read()
	if err != nil {
		return nil, fmt.Errorf("read embedded %s: %w", what, err)
	}
	return data, nil
}
