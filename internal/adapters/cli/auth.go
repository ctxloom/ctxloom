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

const authLong = `How ctxloom-launched engines authenticate, and whether the credential each
auth mode reads is present in your environment.

Each agent declares how its engine authenticates with 'auth:' on its binding
(ctxloom agent edit <name> --auth <mode>):

  token    a long-lived token YOU mint with the engine's own flow and export
           (claude: run 'claude setup-token', then export
           CLAUDE_CODE_OAUTH_TOKEN, or keep it in your secret manager and
           export it from there). The default for an agent that declares
           nothing.
  login    your own login, shared: the same credential and the same
           refresh as your own engine (claude: CLAUDE_SECURESTORAGE_CONFIG_DIR).
           In place on the host; a container mounts it (claude: ~/.claude),
           except on macOS, where it is the Keychain. Refused when missing.
           'ctxloom init' gives the default agent this mode.
  api-key  a pay-per-use key you export (claude: ANTHROPIC_API_KEY).
  cloud    a cloud provider or gateway configured in your own shell (claude:
           Amazon Bedrock, Claude Platform on AWS, Google Vertex, Microsoft
           Foundry, or ANTHROPIC_AUTH_TOKEN with ANTHROPIC_BASE_URL).

ctxloom never collects, stores or mints a credential: it READS the declared
mode's credential from the environment it is launched in and hands it to the
agent (a container receives it the same way). Anthropic does not allow a third
party to "collect, store, or intermediate Claude.ai credentials or session
tokens" (https://code.claude.com/docs/en/legal-and-compliance). A run whose
credential is not exported is refused, naming what to export.

The declared mode decides. Only that mode's credential reaches the engine, and
every other credential the engine reads is removed from the run's environment
— including one you exported yourself. An invalid mode is refused, naming the
modes the engine supports.

Agents on a subscription (token or login) draw from the same usage limits as
your own interactive use: Pro and Max limits are shared across Claude and
Claude Code (https://support.claude.com/en/articles/11145838), and 'claude -p'
draws from the subscription's limits
(https://support.claude.com/en/articles/15036540).

  ctxloom auth status    # per engine and mode: is the credential exported?`

var authCmd = groupNodeDefault(&cobra.Command{
	Use:   "auth",
	Short: "Show how engines authenticate and whether each credential is exported",
	Long:  authLong,
}, "status")

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether each auth mode's credential is present in the environment (never its value)",
	Long:  authLong,
	Args:  cobra.NoArgs,
	RunE:  runAuthStatus,
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
