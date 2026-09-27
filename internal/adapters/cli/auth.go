package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

const authLong = `Store the credentials ctxloom-launched engines authenticate with.

Each agent declares how its engine authenticates with 'auth:' on its binding
(ctxloom agent edit <name> --auth <mode>):

  login    your own login, in place: the same credential and the same
           refresh as your own engine (claude: CLAUDE_SECURESTORAGE_CONFIG_DIR).
           Host runs only. 'ctxloom init' gives the default agent this mode.
  token    a long-lived token the engine mints (claude: 'claude setup-token',
           a year, never refreshed). The default for an agent that declares
           nothing.
  api-key  a pay-per-use key you supply (claude: ANTHROPIC_API_KEY).
  cloud    a cloud provider or gateway configured in your own shell (claude:
           Amazon Bedrock, Claude Platform on AWS, Google Vertex, Microsoft
           Foundry, or ANTHROPIC_AUTH_TOKEN with ANTHROPIC_BASE_URL). Nothing
           is stored or minted for it.

The declared mode decides. Only that mode's credential reaches the engine: a
value you export for THAT mode wins over the stored one, and every other
credential the engine reads is removed from the run's environment — including
one you exported yourself. An invalid mode is refused, naming the modes the
engine supports.

ctxloom mints a token the first time a run needs one, at your terminal, and
stores it owner-only under ~/.ctxloom/auth. A run with no terminal (a
delegated agent, an unattended run) never prompts: it is refused, naming
'ctxloom auth mint'. Credentials never enter a config file or a repository.

  ctxloom auth mint --mode token     # run the engine's own mint flow, store the token
  ctxloom auth set --mode api-key    # paste a key; read from stdin, never argv
  ctxloom auth status                # what is stored, and who can read it`

var authCmd = groupNodeDefault(&cobra.Command{
	Use:   "auth",
	Short: "Store the credentials ctxloom-launched engines authenticate with",
	Long:  authLong,
}, "status")

var (
	authEngine string
	authMode   string
)

// errSecretOnArgv refuses a credential typed as an argument, without
// repeating it: an argument lands in shell history and the process list.
var errSecretOnArgv = errors.New("a credential is read from stdin, never taken as an argument (it would land in shell history and the process list); nothing was stored")

// noSecretArgs is the Args check for the credential commands: it refuses any
// positional argument WITHOUT echoing it, which cobra.NoArgs would do.
func noSecretArgs(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return errSecretOnArgv
	}
	return nil
}

var authMintCmd = &cobra.Command{
	Use:   "mint",
	Short: "Run the engine's own flow to mint a credential, and store it owner-only",
	Long: authLong + `

mint runs the engine's interactive flow on this terminal (claude opens a
browser for 'claude setup-token') and stores what it produces. A mode the
engine cannot mint (an API key) is refused; store one with 'ctxloom auth set'.`,
	Args: noSecretArgs,
	RunE: runAuthMint,
}

var authSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Store a credential you already have (read from stdin, never argv)",
	Long: authLong + `

The credential is read from stdin: pasted at a hidden prompt on a terminal, or
piped in. It is never taken as an argument, so it stays out of shell history
and the process list, and it is never printed.`,
	Args: noSecretArgs,
	RunE: runAuthSet,
}

// authStoreResult is the emitted shape of `auth mint` and `auth set`. It
// carries no credential.
type authStoreResult struct {
	Engine string `json:"engine" yaml:"engine" toml:"engine"`
	Mode   string `json:"mode" yaml:"mode" toml:"mode"`
	Path   string `json:"path" yaml:"path" toml:"path"`
}

// authTarget is the engine and mode a credential command acts on: the
// engine's declared auth and the parsed mode, refused when the engine lacks
// it.
func authTarget() (string, engine.Auth, engine.AuthMode, error) {
	name := authEngine
	if name == "" {
		name = operations.DefaultEngineName(App().Engines())
	}
	kind, ok := App().Engines().Lookup(engine.Name(name))
	if !ok {
		return "", nil, "", fmt.Errorf("%s: %w", name, isolation.ErrNoAuth)
	}
	mode, err := engine.CheckAuth(kind.Root().Name, kind.Home().Auth, authMode)
	if err != nil {
		return "", nil, "", err
	}
	a, ok := kind.Home().Auth.Get()
	if !ok {
		return "", nil, "", fmt.Errorf("%s: %w", name, isolation.ErrNoAuth)
	}
	return name, a, mode, nil
}

// authTerminal is the terminal `auth mint` runs the engine's flow on: the
// command's own stdin when it is one, with the flow's output on stderr so
// stdout stays the command's result. A package var so a test hands in a
// fake terminal.
var authTerminal = func(cmd *cobra.Command) (engine.Terminal, bool) {
	f, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return engine.Terminal{}, false
	}
	return engine.Terminal{In: f, Out: cmd.ErrOrStderr(), Err: cmd.ErrOrStderr()}, true
}

// errMintNeedsTerminal: mint's flow is interactive and this run has no
// terminal to drive it on.
var errMintNeedsTerminal = errors.New("auth mint runs the engine's interactive flow and needs a terminal on stdin")

func runAuthMint(cmd *cobra.Command, _ []string) error {
	name, a, mode, err := authTarget()
	if err != nil {
		return err
	}
	t, ok := authTerminal(cmd)
	if !ok {
		return errMintNeedsTerminal
	}
	secret, err := a.Mint(cmd.Context(), mode, t)
	if errors.Is(err, engine.ErrMintUnsupported) {
		fix := fmt.Sprintf("store one you already have with `ctxloom auth set --engine %s --mode %s`", name, mode)
		if !mode.Stored() {
			fix = fmt.Sprintf("nothing is minted or stored for auth %s: it uses what your own shell or login already holds", mode)
		}
		return report.Errorf(fix, "%s auth %s: %w", name, mode, err)
	}
	if err != nil {
		return err
	}
	return storeAndEmit(cmd, name, mode, secret)
}

func runAuthSet(cmd *cobra.Command, _ []string) error {
	name, _, mode, err := authTarget()
	if err != nil {
		return err
	}
	secret, err := readSecret(cmd, fmt.Sprintf("Paste the %s %s: ", name, mode))
	if err != nil {
		return err
	}
	return storeAndEmit(cmd, name, mode, secret)
}

func storeAndEmit(cmd *cobra.Command, name string, mode engine.AuthMode, secret []byte) error {
	path, err := isolation.StoreEngineCredential(name, mode, secret)
	if err != nil {
		return err
	}
	res := authStoreResult{Engine: name, Mode: string(mode), Path: path}
	return emit(cmd, res, func() error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "stored the %s %s credential in %s (owner-only); agents declaring auth: %s get it unless you export your own\n",
			res.Engine, res.Mode, res.Path, res.Mode)
		return err
	})
}

// maxSecretInput bounds what set reads: a credential is a few hundred bytes,
// and an unbounded read of a mistaken pipe is not one.
const maxSecretInput = 64 << 10

// readSecret reads the credential at a no-echo prompt when stdin is the
// terminal, else from whatever is piped in.
func readSecret(cmd *cobra.Command, prompt string) ([]byte, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), prompt)
		s, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		return s, err
	}
	return io.ReadAll(io.LimitReader(in, maxSecretInput))
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show which credentials are stored and who can read them (never the credential)",
	Long:  authLong,
	Args:  cobra.NoArgs,
	RunE:  runAuthStatus,
}

// authStatusRow is the emitted shape of one `auth status` row. Protection is
// this platform's verdict on who can read the file: its mode on unix, the
// ACL's owner-only or exposed verdict on Windows.
type authStatusRow struct {
	Engine     string `json:"engine" yaml:"engine" toml:"engine"`
	Mode       string `json:"mode" yaml:"mode" toml:"mode"`
	Path       string `json:"path" yaml:"path" toml:"path"`
	Stored     bool   `json:"stored" yaml:"stored" toml:"stored"`
	Protection string `json:"protection,omitempty" yaml:"protection,omitempty" toml:"protection,omitempty"`
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	all, err := isolation.EngineCredentialStatuses()
	if err != nil {
		return err
	}
	rows := make([]authStatusRow, 0, len(all))
	for _, s := range all {
		rows = append(rows, authStatusRow{Engine: s.Engine, Mode: string(s.Mode), Path: s.Path, Stored: s.Stored, Protection: s.Protection})
	}
	return emit(cmd, rows, func() error {
		w := cmd.OutOrStdout()
		for _, r := range rows {
			line := fmt.Sprintf("%s %s: none stored at %s\n", r.Engine, r.Mode, r.Path)
			if r.Stored {
				line = fmt.Sprintf("%s %s: stored at %s (%s)\n", r.Engine, r.Mode, r.Path, r.Protection)
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
	authCmd.AddCommand(authMintCmd, authSetCmd, authStatusCmd)
	for _, c := range []*cobra.Command{authMintCmd, authSetCmd} {
		c.Flags().StringVar(&authEngine, "engine", "", "engine the credential is for")
		c.Flags().StringVar(&authMode, "mode", "", "auth mode the credential is for: "+strings.Join(engine.AuthModeNames(), ", "))
		_ = c.MarkFlagRequired("mode")
	}
}
