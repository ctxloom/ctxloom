package operations

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/resources"
)

// InitializeProjectRequest is the input for InitializeProject.
type InitializeProjectRequest struct {
	AppDir string `json:"app_dir"`
	Engine string `json:"engine"`

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
func InitializeProject(_ context.Context, req InitializeProjectRequest) (*InitializeProjectResult, error) {
	if req.AppDir == "" {
		return nil, fmt.Errorf("app dir is required")
	}
	fs := getFS(req.FS)
	for _, dir := range []string{req.AppDir, filepath.Join(req.AppDir, paths.ProfilesDir), paths.BundlesPath(req.AppDir)} {
		if err := fs.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	configData, err := BuildInitialConfig(req.Engine)
	if err != nil {
		return nil, fmt.Errorf("failed to build config.yaml: %w", err)
	}
	if err := afero.WriteFile(fs, paths.ConfigPath(req.AppDir), configData, 0644); err != nil {
		return nil, fmt.Errorf("failed to create config.yaml: %w", err)
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
func BuildInitialConfig(engine string) ([]byte, error) {
	scaffold, err := config.ParseConfig(mustResource(resources.GetInitConfig))
	if err != nil {
		return nil, fmt.Errorf("parse init scaffold: %w", err)
	}

	registry, err := config.ParseConfig(mustResource(resources.GetDefaultConfig))
	if err != nil {
		return nil, fmt.Errorf("parse default registry: %w", err)
	}

	scaffold.LM = engineRegistry(engine, registry.LM)

	// Bind the always-bound DEFAULT AGENT to the scaffolded seed profile, carrying
	// the selected engine's primary label so a bare `ctxloom run` launches the same
	// backend. This replaces the retired profiles.defaults: the default context is
	// now whatever the default agent composes (Config.DefaultAgentProfiles).
	scaffold.DefaultAgent = SeedProfileName
	scaffold.Agents = map[string]agents.Agent{
		SeedProfileName: {
			Engine:   scaffold.PrimaryLabel(),
			Runtime:  "host",
			Profiles: []string{SeedProfileName},
		},
	}
	return scaffold.Marshal()
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

// mustResource reads an embedded resource, returning nil bytes on error. The
// embedded files always exist, so an error here means a build/embed problem;
// ParseConfig of nil bytes yields an empty config, which the caller surfaces.
func mustResource(read func() ([]byte, error)) []byte {
	data, _ := read()
	return data
}
