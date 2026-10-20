package operations

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMaterialize_CorruptFileRefusalClaimsNoBackup: a materialize meeting a
// file it cannot read refuses without claiming a backup, in a dry run and in
// an apply, on the real filesystem and in memory. Every approach runs over
// the static writer's overlay, which the refusal discards — a backup written
// through it never lands, so the refusal says the original is left untouched
// and no .corrupt-* stands beside it.
// MUTATION: the claude approach's writer refuses with agent.RefuseCorrupt →
// red (the reason names a backup that is not there).
func TestMaterialize_CorruptFileRefusalClaimsNoBackup(t *testing.T) {
	files := map[string]func(t *testing.T) (afero.Fs, safefs.Root, string){
		"os": func(t *testing.T) (afero.Fs, safefs.Root, string) {
			return afero.NewOsFs(), safefs.New(), t.TempDir()
		},
		"mem": func(t *testing.T) (afero.Fs, safefs.Root, string) {
			fs := afero.NewMemMapFs()
			return fs, safefs.NewMem(fs), "/project"
		},
	}
	corrupt := []struct {
		name, rel, body string
	}{
		{"settings.json permissions.deny", claudeSettings, `{"permissions":{"deny":"nope"}}`},
		{"settings.json env", claudeSettings, `{"env":"not-an-object"}`},
		{"settings.json whole", claudeSettings, `{"permissions":`},
	}
	for fsName, mk := range files {
		for _, c := range corrupt {
			for _, dry := range []bool{true, false} {
				t.Run(fsName+"/"+c.name+"/"+map[bool]string{true: "dry", false: "apply"}[dry], func(t *testing.T) {
					fs, root, dir := mk(t)
					kind := lookupEngine(t, "claude-code")
					path := filepath.Join(dir, filepath.FromSlash(c.rel))
					testsupport.WriteFileString(t, fs, path, c.body, 0o644)
					pkg := everyKindPackage(t, "body")
					pkg.ShellTimeout = engine.ShellTimeout{Default: time.Minute, Max: 2 * time.Minute}
					var out EngineOutcome
					p := atRestPlacement(dir, kind.Root().Name, delivery.AllKinds())

					var reasons []string
					if dry {
						_, err := previewMaterializedAt(context.Background(), root, kind, pkg, p, &out)
						require.NoError(t, err)
						for _, r := range out.Refused {
							reasons = append(reasons, r.Reason)
						}
					} else {
						deliverMaterializedAt(context.Background(), root, kind, pkg, p, &out)
						reasons = out.Warnings
					}

					require.NotEmpty(t, reasons, "the corrupt file is refused")
					joined := strings.Join(reasons, "\n")
					assert.Contains(t, joined, agent.CorruptLeftUntouched, "the refusal says the original is left untouched")
					assert.NotContains(t, joined, agent.CorruptBackedUp, "the refusal claims no backup")
					backups, err := afero.Glob(fs, path+".corrupt-*")
					require.NoError(t, err)
					assert.Empty(t, backups, "no backup stands beside the original")
					got, err := afero.ReadFile(fs, path)
					require.NoError(t, err)
					assert.Equal(t, c.body, string(got), "the original is left untouched")
				})
			}
		}
	}
}

// TestMaterialize_CorruptMCPFileClaimsNoBackup probes the claude .mcp.json:
// its approach never reads the file through agent.RefuseCorrupt (the static
// writer's own read refuses it), so a materialize names no backup there
// either, whether the file does not parse or its mcpServers is no object.
func TestMaterialize_CorruptMCPFileClaimsNoBackup(t *testing.T) {
	for _, body := range []string{`{"mcpServers": `, `{"mcpServers": "nope"}`} {
		for _, dry := range []bool{true, false} {
			fs := afero.NewMemMapFs()
			dir := "/project"
			kind := lookupEngine(t, "claude-code")
			path := filepath.Join(dir, claude.MCPFileName)
			testsupport.WriteFileString(t, fs, path, body, 0o644)
			var out EngineOutcome
			p := atRestPlacement(dir, kind.Root().Name, delivery.AllKinds())
			var reasons []string
			if dry {
				_, err := previewMaterializedAt(context.Background(), safefs.NewMem(fs), kind, everyKindPackage(t, "body"), p, &out)
				require.NoError(t, err)
				for _, r := range out.Refused {
					reasons = append(reasons, r.Reason)
				}
			} else {
				deliverMaterializedAt(context.Background(), safefs.NewMem(fs), kind, everyKindPackage(t, "body"), p, &out)
				reasons = out.Warnings
			}
			require.NotEmpty(t, reasons, "the corrupt .mcp.json is refused: %s", body)
			assert.NotContains(t, strings.Join(reasons, "\n"), agent.CorruptBackedUp, "the refusal claims no backup")
			backups, err := afero.Glob(fs, path+".corrupt-*")
			require.NoError(t, err)
			assert.Empty(t, backups)
		}
	}
}

// TestMaterialize_ReportsAStatuslineItDidNotWrite: a statusline ctxloom did
// not write — the user's own, or an earlier ctxloom's spelling — is left in
// place, and the materialize says so as a finding (StatuslineNotOurs), in a
// dry run and an apply, without failing; with ctxloom's own statusline, or
// none, there is no such finding.
// MUTATION: SettingsInputs.Report not wired from the loadout → red.
func TestMaterialize_ReportsAStatuslineItDidNotWrite(t *testing.T) {
	ours := `{"statusLine": {"type": "command", "command": "ctxloom hook hud"}}`
	for name, c := range map[string]struct {
		body    string
		finding bool
	}{
		"the user's own":     {`{"statusLine": {"type": "command", "command": "my-hud"}}`, true},
		"an older ctxloom's": {`{"statusLine": {"type": "command", "command": "/usr/local/bin/ctxloom hook hud"}}`, true},
		"ctxloom's own":      {ours, false},
		"none":               {"", false},
	} {
		for _, dry := range []bool{true, false} {
			t.Run(name+"/"+map[bool]string{true: "dry", false: "apply"}[dry], func(t *testing.T) {
				fs := afero.NewMemMapFs()
				dir := "/project"
				kind := lookupEngine(t, "claude-code")
				path := filepath.Join(dir, filepath.FromSlash(claudeSettings))
				if c.body != "" {
					testsupport.WriteFileString(t, fs, path, c.body, 0o644)
				}
				var out EngineOutcome
				p := atRestPlacement(dir, kind.Root().Name, delivery.AllKinds())
				if dry {
					_, err := previewMaterializedAt(context.Background(), safefs.NewMem(fs), kind, everyKindPackage(t, "body"), p, &out)
					require.NoError(t, err)
					assert.Empty(t, out.Refused)
				} else {
					deliverMaterializedAt(context.Background(), safefs.NewMem(fs), kind, everyKindPackage(t, "body"), p, &out)
					assert.Empty(t, out.Warnings, "the run succeeds")
				}
				var found []string
				for _, s := range out.Skipped {
					if strings.Contains(s, claude.StatuslineNotOurs) {
						found = append(found, s)
					}
				}
				if !c.finding {
					assert.Empty(t, found)
					return
				}
				require.Len(t, found, 1, "reported once")
				doc := jsonDoc(t, fs, dir, claudeSettings)
				var want map[string]any
				require.NoError(t, json.Unmarshal([]byte(c.body), &want))
				assert.Equal(t, want["statusLine"], doc["statusLine"], "the statusline is left in place")
			})
		}
	}
}
