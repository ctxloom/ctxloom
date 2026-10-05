package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// authLong documents `ctxloom auth`. Why it reads rather than collects:
// Anthropic does not allow a third party to "collect, store, or intermediate
// Claude.ai credentials or session tokens"
// (https://code.claude.com/docs/en/legal-and-compliance), so ctxloom only
// reads the mode's credential from its environment and hands it to the engine
// (a container receives the token as a secret file). init stops after minting
// because a child process cannot set the user's shell environment. A
// subscription's limits are shared across Claude and Claude Code
// (https://support.claude.com/en/articles/11145838), and 'claude -p' draws
// from them (https://support.claude.com/en/articles/15036540).
const authLong = `How ctxloom-launched engines authenticate, and whether the credential each
auth mode reads is exported.

Every agent ctxloom spawns (a delegated child, a one-shot, a container cell)
uses one long-lived token you mint and export: for claude, run
'claude setup-token' and export CLAUDE_CODE_OAUTH_TOKEN, or export it from
your secret manager. 'ctxloom init' checks for it and runs that flow for you.

Your own 'ctxloom run' session uses the top-level 'auth:' in your config:

  token    that same token; init writes this, and it is the default
  login    your own engine login, shared in place; host only

ctxloom never collects, stores or mints a credential. It reads the mode's
credential from its environment, strips every other engine credential (API
keys, gateway tokens, cloud-provider switches) from the run, and refuses a
run whose credential is not exported, naming what to export. Agents on a
subscription share your usage limits.`

var authCmd = groupNodeDefault(&cobra.Command{
	Use:   "auth",
	Short: "Show how engines authenticate and whether each credential is exported",
	Long:  authLong,
	Example: `  ctxloom auth
  ctxloom auth status`,
}, "status")

var authStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show whether each auth mode's credential is present in the environment (never its value)",
	Long:    authLong,
	Example: `  ctxloom auth status`,
	Args:    cobra.NoArgs,
	RunE:    runAuthStatus,
}

// authStatusRow is the emitted shape of one `auth status` row: whether the
// mode's credential is present in the environment, the variables it is read
// from when it is (never their values), and the engine's remedy when it is
// not.
type authStatusRow struct {
	Engine  string   `json:"engine" yaml:"engine" toml:"engine"`
	Mode    string   `json:"mode" yaml:"mode" toml:"mode"`
	Present bool     `json:"present" yaml:"present" toml:"present"`
	Vars    []string `json:"vars,omitempty" yaml:"vars,omitempty" toml:"vars,omitempty"`
	Remedy  string   `json:"remedy,omitempty" yaml:"remedy,omitempty" toml:"remedy,omitempty"`
}

// authStatusRows asks each engine that declares auth, for each of its modes,
// what a run would get from the environment. A mode that resolves to no
// variable at all (a login shared in place) reads nothing from the
// environment and has no row.
func authStatusRows(reg engine.Registry, shell func(string) (string, bool)) ([]authStatusRow, error) {
	var rows []authStatusRow
	for _, name := range operations.EngineNames(reg) {
		kind, _ := reg.Lookup(engine.Name(name))
		a, ok := kind.Home().Auth.Get()
		if !ok {
			continue
		}
		for _, mode := range a.Modes() {
			row := authStatusRow{Engine: name, Mode: string(mode)}
			creds, err := a.Credentials(mode, shell)
			switch {
			case errors.Is(err, engine.ErrNoCredential):
				var r report.Remediable
				if errors.As(err, &r) {
					row.Remedy = r.Remedy()
				}
			case err != nil:
				return nil, err
			case len(creds.Env) == 0:
				continue
			default:
				row.Present = true
				for k := range creds.Env {
					row.Vars = append(row.Vars, k)
				}
				slices.Sort(row.Vars)
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	rows, err := authStatusRows(App().Engines(), os.LookupEnv)
	if err != nil {
		return err
	}
	return emit(cmd, rows, func() error {
		w := cmd.OutOrStdout()
		for _, r := range rows {
			line := fmt.Sprintf("%s %s: missing — %s\n", r.Engine, r.Mode, r.Remedy)
			if r.Present {
				line = fmt.Sprintf("%s %s: present (%s)\n", r.Engine, r.Mode, strings.Join(r.Vars, ", "))
			}
			if _, err := io.WriteString(w, line); err != nil {
				return err
			}
		}
		return nil
	})
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authStatusCmd)
}
