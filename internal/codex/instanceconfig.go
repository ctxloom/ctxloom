package codex

import (
	"fmt"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// elidedHostSections are the config.toml top-level tables that must NOT cross
// from the user's real ~/.codex into an instance — the codex half of the
// allow-list principle, expressed as a section-level elision because a TOML
// config is a handful of named tables rather than a flat key set.
//
//   - mcp_servers: on a real host this carries the user's OWN personal MCP
//     registrations and whatever secrets their env blocks hold. Copying it in
//     would hand every agent read access to the human's private integrations —
//     the identical leak claude's `.claude.json` wholesale copy caused, and the
//     reason that copy was dropped. removeManagedMCP deliberately does not
//     strip user entries, so eliding here is the only place this can be caught.
//   - hooks: a hook is an arbitrary command line the user configured for their
//     own interactive sessions. An agent run that inherits it executes the
//     human's commands on the agent's schedule, with the agent's cwd.
//
// Everything ELSE the user set — model,
// approval_policy, sandbox, profiles — is exactly what makes copying the file
// worth doing: those preferences then apply to every agent session for free.
var elidedHostSections = []string{"mcp_servers", "hooks"}

// NewInstanceConfigWriter constructs codex's instance-config generator — the
// engine-owned seeder of `<CODEX_HOME>/config.toml`'s BASE from the user's real
// `~/.codex/config.toml`, minus the sections above. It is codex's half of the
// engine write-config directive: isolation.CopyAmbient decides that codex
// contributes a generated base at all, this owns every byte of the TOML.
func NewInstanceConfigWriter(o agent.SettingsOptions) agent.InstanceConfigWriter {
	return &codexInstanceConfig{FS: o.FS}
}

// codexInstanceConfig implements agent.InstanceConfigWriter for codex.
type codexInstanceConfig struct {
	FS afero.Fs
}

var _ agent.InstanceConfigWriter = (*codexInstanceConfig)(nil)

// WriteInstanceConfig seeds the instance's config.toml BASE with the user's own
// ~/.codex/config.toml, minus elidedHostSections. ctxloom's managed [hooks] and
// [mcp_servers] tables and the `[projects."<abs WorkDir>"]` trust pre-seed are
// written ON TOP of it later, by writeSettingsIn on the delivery path — this
// only establishes the base, which is why it does not need WorkDir.
//
// SEED-ONCE, by design. A destination that already exists is left alone: by
// then it carries ctxloom's managed sections and the trust pre-seed from an
// earlier run in this same session, and re-seeding from the host would drop
// them for however long it takes the delivery path to put them back. The base
// is a base — establishing it twice is not more correct, only more destructive.
//
// An absent host config.toml is ordinary (a user who never wrote one) and
// contributes nothing, silently. An UNPARSEABLE one warns and contributes
// nothing: it is the user's file, ctxloom neither owns nor repairs it, and a
// run is perfectly viable without their preferences.
func (w *codexInstanceConfig) WriteInstanceConfig(req agent.InstanceConfigRequest) (agent.InstanceConfigReport, error) {
	var rep agent.InstanceConfigReport
	if req.InstanceHome == "" {
		return rep, fmt.Errorf("codex instance config: no instance home to seed %s in", ConfigFileName)
	}
	if req.HostHome == "" {
		return rep, nil // no ambient source; the managed sections still land on the delivery path
	}
	fs := agent.GetFS(w.FS)
	writer := &CodexHookWriter{FS: w.FS}
	dest := writer.settingsPathIn(req.InstanceHome)

	if exists, err := afero.Exists(fs, dest); err != nil {
		return rep, fmt.Errorf("codex instance config: cannot determine whether %s exists: %w", dest, err)
	} else if exists {
		return rep, nil
	}

	hostPath := filepath.Join(req.HostHome, ConfigDirName, ConfigFileName)
	data, err := afero.ReadFile(fs, hostPath)
	if err != nil {
		return rep, nil // absent (or unreadable) host config: nothing ambient to carry
	}
	cfg := map[string]any{}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"cannot parse the host %s (%v), so none of your codex preferences (model, approval_policy, sandbox, profiles) apply to this run", hostPath, err))
		return rep, nil
	}
	for _, section := range elidedHostSections {
		delete(cfg, section)
	}
	if len(cfg) == 0 {
		// Everything the host file held was elided, or it was empty. Writing a
		// zero-byte config.toml here would be a success report over no bytes at
		// all — this project's characteristic false green — and the delivery
		// path creates the file itself moments later anyway.
		return rep, nil
	}
	if err := fs.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return rep, fmt.Errorf("codex instance config: create %s: %w", filepath.Dir(dest), err)
	}
	if err := writer.save(dest, cfg, false); err != nil {
		return rep, fmt.Errorf("codex instance config: write %s: %w", dest, err)
	}
	rep.Wrote = append(rep.Wrote, dest)
	return rep, nil
}
