package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
)

const authLong = `Store the long-lived token ctxloom-launched engines authenticate with.

Every claude that ctxloom launches, on the host or in a container, top-level or
delegated, authenticates with one token from 'claude setup-token'. That token
lasts a year, claude never refreshes it and never writes it to disk, so every
run can share it safely. ctxloom does not copy your ~/.claude login anywhere.

  claude setup-token          # prints the token once
  ctxloom auth set-token      # paste it; stored owner-only under ~/.ctxloom/auth

ctxloom exports the stored token as CLAUDE_CODE_OAUTH_TOKEN to every run. If
you already export CLAUDE_CODE_OAUTH_TOKEN yourself, yours wins and the stored
one is not used.`

var authCmd = groupNodeDefault(&cobra.Command{
	Use:   "auth",
	Short: "Store the token ctxloom-launched engines authenticate with",
	Long:  authLong,
}, "status")

var authSetTokenEngine string

var authSetTokenCmd = &cobra.Command{
	Use:   "set-token",
	Short: "Store the token 'claude setup-token' printed (read from stdin, never argv)",
	Long: authLong + `

The token is read from stdin: pasted at a hidden prompt on a terminal, or piped
in. It is never taken as an argument, so it stays out of shell history and the
process list, and it is never printed.`,
	Args: cobra.NoArgs,
	RunE: runAuthSetToken,
}

// authSetTokenResult is the emitted shape of `auth set-token`. It carries no
// token.
type authSetTokenResult struct {
	Engine string `json:"engine" yaml:"engine" toml:"engine"`
	Var    string `json:"var" yaml:"var" toml:"var"`
	Path   string `json:"path" yaml:"path" toml:"path"`
}

func runAuthSetToken(cmd *cobra.Command, _ []string) error {
	name := authSetTokenEngine
	if name == "" {
		name = operations.DefaultEngineName()
	}
	a, ok := isolation.TokenAuthFor(name)
	if !ok {
		return fmt.Errorf("%s: %w", name, isolation.ErrNoTokenAuth)
	}
	tok, err := readToken(cmd, fmt.Sprintf("Paste the token `%s` printed: ", a.MintHint))
	if err != nil {
		return err
	}
	path, err := isolation.StoreEngineToken(name, tok)
	if err != nil {
		return err
	}
	res := authSetTokenResult{Engine: name, Var: a.TokenVar, Path: path}
	return emit(cmd, res, func() error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "stored the %s token in %s (owner-only); runs get it as %s unless you export your own\n",
			res.Engine, res.Path, res.Var)
		return err
	})
}

// maxTokenInput bounds what set-token reads: a token is a few hundred bytes,
// and an unbounded read of a mistaken pipe is not one.
const maxTokenInput = 64 << 10

// readToken reads the token at a no-echo prompt when stdin is the terminal,
// else from whatever is piped in.
func readToken(cmd *cobra.Command, prompt string) ([]byte, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), prompt)
		tok, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		return tok, err
	}
	return io.ReadAll(io.LimitReader(in, maxTokenInput))
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether a token is stored and which one runs get (never the token)",
	Long:  authLong,
	Args:  cobra.NoArgs,
	RunE:  runAuthStatus,
}

// authStatusRow is the emitted shape of one `auth status` row.
type authStatusRow struct {
	Engine string `json:"engine" yaml:"engine" toml:"engine"`
	Var    string `json:"var" yaml:"var" toml:"var"`
	Path   string `json:"path" yaml:"path" toml:"path"`
	Stored bool   `json:"stored" yaml:"stored" toml:"stored"`
	Mode   string `json:"mode,omitempty" yaml:"mode,omitempty" toml:"mode,omitempty"`
	Source string `json:"source" yaml:"source" toml:"source"`
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	all, err := isolation.EngineTokenStatuses()
	if err != nil {
		return err
	}
	rows := make([]authStatusRow, 0, len(all))
	for _, s := range all {
		r := authStatusRow{Engine: s.Engine, Var: s.Var, Path: s.Path, Stored: s.Stored, Source: string(s.Source)}
		if s.Stored {
			r.Mode = fmt.Sprintf("%04o", s.Mode)
		}
		rows = append(rows, r)
	}
	return emit(cmd, rows, func() error {
		w := cmd.OutOrStdout()
		for _, r := range rows {
			stored := "no token stored at " + r.Path
			if r.Stored {
				stored = fmt.Sprintf("token stored at %s (mode %s)", r.Path, r.Mode)
			}
			used := map[string]string{
				string(isolation.TokenSourceEnv):    "runs get your exported " + r.Var,
				string(isolation.TokenSourceStored): "runs get the stored token as " + r.Var,
				string(isolation.TokenSourceNone):   r.Var + " is unset",
			}[r.Source]
			if _, err := fmt.Fprintf(w, "%s: %s; %s\n", r.Engine, stored, used); err != nil {
				return err
			}
		}
		return nil
	})
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authSetTokenCmd)
	authCmd.AddCommand(authStatusCmd)
	authSetTokenCmd.Flags().StringVar(&authSetTokenEngine, "engine", "", "engine the token is for")
}
