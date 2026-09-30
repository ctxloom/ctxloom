package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// AuthMode is HOW an agent's engine authenticates, in words every engine
// shares. Which env vars carry each mode, which one the engine reads first,
// and how a credential is minted are the ENGINE's answers (Auth); the modes
// themselves are vocabulary, so an agent binding can name one without
// knowing which engine it will run on.
type AuthMode string

const (
	// AuthLogin: the human's own login, in place. Nothing is stored by
	// ctxloom for it: the engine is pointed at the credential the human's own
	// engine already keeps.
	AuthLogin AuthMode = "login"
	// AuthToken: a long-lived token the engine mints (Auth.Mint), stored
	// owner-only by ctxloom and handed to the engine's env.
	AuthToken AuthMode = "token"
	// AuthAPIKey: a pay-per-use API key the human supplies, stored owner-only
	// by ctxloom and handed to the engine's env.
	AuthAPIKey AuthMode = "api-key"
	// AuthCloud: a cloud provider or gateway the human has configured in
	// their own shell (the engine names which variables). Passed through,
	// never stored and never minted.
	AuthCloud AuthMode = "cloud"
)

// AuthModeNames lists the accepted `auth` values, for flag help, shell
// completion and error messages.
func AuthModeNames() []string {
	return []string{string(AuthLogin), string(AuthToken), string(AuthAPIKey), string(AuthCloud)}
}

// ParseAuthMode turns a binding's declared `auth` into its effective mode.
// Undeclared is AuthToken: the default never reaches the human's own login,
// which a binding selects by name. An unknown spelling is refused rather
// than defaulted, because it would silently pick a credential nobody chose.
func ParseAuthMode(declared string) (AuthMode, error) {
	switch m := AuthMode(strings.TrimSpace(declared)); m {
	case "":
		return AuthToken, nil
	case AuthLogin, AuthToken, AuthAPIKey, AuthCloud:
		return m, nil
	default:
		return "", report.Errorf("declare one of "+strings.Join(AuthModeNames(), ", "), "auth %q: %w", declared, ErrUnknownAuthMode)
	}
}

// CheckAuth is the ONE check of an agent's auth selection against the
// engine it binds: config load, `agent create/edit` and every launch run
// it, so the three can never disagree. declared is the binding's `auth:` as
// written ("" when undeclared, which is the token for an engine with auth
// and nothing for one without). Every refusal is typed and carries a remedy
// naming what to do, the modes derived from THIS engine's Auth.Modes.
func CheckAuth(eng Name, declared Declared[Auth], mode string) (AuthMode, error) {
	mode = strings.TrimSpace(mode)
	a, ok := declared.Get()
	if !ok {
		if mode == "" {
			return "", nil
		}
		return "", report.Errorf(fmt.Sprintf("remove `auth: %s` from the agent: %s authenticates on its own (%s)", mode, eng, declared.AbsentReason()),
			"%s: auth %q: %w", eng, mode, ErrEngineHasNoAuth)
	}
	supported := strings.Join(modeNames(a.Modes()), ", ")
	m, err := ParseAuthMode(mode)
	if err != nil {
		return "", report.Errorf("declare one of the modes "+string(eng)+" supports: "+supported, "%s: auth %q: %w", eng, mode, ErrUnknownAuthMode)
	}
	if !SupportsMode(a, m) {
		return "", report.Errorf("declare one of the modes "+string(eng)+" supports: "+supported, "%s: auth %s: %w", eng, m, ErrAuthModeUnsupported)
	}
	return m, nil
}

func modeNames(modes []AuthMode) []string {
	out := make([]string, len(modes))
	for i, m := range modes {
		out[i] = string(m)
	}
	return out
}

// Stored reports whether ctxloom keeps a credential for the mode. The
// human's login is theirs, held where their own engine keeps it, and a
// cloud provider's credentials are theirs, in their own shell; a token and
// an API key are ctxloom's to store.
func (m AuthMode) Stored() bool { return m == AuthToken || m == AuthAPIKey }

// Minted reports whether the mode's credential is one the engine mints
// (Auth.Mint) rather than one the human supplies: only the token.
func (m AuthMode) Minted() bool { return m == AuthToken }

var (
	// ErrNoCredential: the mode needs a credential and neither the launching
	// env nor the store holds one. Returned (wrapped) by Auth.Credentials and
	// by a CredentialReader.
	ErrNoCredential = errors.New("no credential for this auth mode")
	// ErrMintUnsupported: the engine cannot mint a credential for the mode;
	// the human supplies one instead.
	ErrMintUnsupported = errors.New("the engine cannot mint a credential for this auth mode")
	// ErrAuthModeUnsupported: the engine does not authenticate in the mode.
	ErrAuthModeUnsupported = errors.New("the engine does not support this auth mode")
	// ErrUnknownAuthMode: the declared mode is not in the shared vocabulary.
	ErrUnknownAuthMode = errors.New("unknown auth mode")
	// ErrEngineHasNoAuth: an auth mode is declared for an engine that
	// declares no auth at all.
	ErrEngineHasNoAuth = errors.New("the engine declares no auth")
)

// CredentialReader is one engine's stored credentials, read by mode. A mode
// with nothing stored returns ErrNoCredential; a credential that is stored
// but unsafe to use (readable by others) is an error of its own, never
// silently treated as absent.
type CredentialReader interface {
	Read(mode AuthMode) ([]byte, error)
}

// Terminal is the human's interactive terminal: what a mint's interactive
// flow reads from and writes to.
type Terminal struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Credentials is what a run in one auth mode needs, as runtime-neutral DATA:
// the environment that prepares the run decides how each piece is made true
// where the engine runs, and the engine never learns which environment that
// was.
type Credentials struct {
	// Env is laid over the engine's environment: the mode's credential.
	Env map[string]string
	// Unset names variables removed from the engine's environment before it
	// starts. It exists because an empty value is not an absent one: for some
	// variables "" means a real default (claude reads an empty
	// credential-storage var as $HOME/.claude), and some switches are read as
	// set whatever their value.
	Unset []string
	// Stores are the human's own credential stores the mode reads in place —
	// a login's storage, a cloud provider's credential files. Each must exist
	// where the run starts; an environment that cannot present one refuses.
	Stores []SharedStore
	// FileVars names the Env variables whose value is the host path of a
	// credential FILE the mode reads (a cloud provider's config or key
	// file). The value is the human's, never rewritten here: an environment
	// that runs the engine where that path means nothing presents the file
	// read-only and points the variable at it.
	FileVars []string
}

// SharedStore is one of the human's credential stores a run shares rather
// than copies: a directory the engine (or the provider SDK it embeds) reads,
// and writes unless ReadOnly.
type SharedStore struct {
	// Var is the variable that points the engine at the store, "" when the
	// engine finds it at HomeRel under $HOME with no variable at all.
	Var string
	// Value is the exact string the launching env's engine resolves Var
	// from, never cleaned: claude names its macOS keychain item from it. ""
	// means the engine's default, $HOME/HomeRel.
	Value string
	// HomeRel is where the engine keeps the store under $HOME when Var is
	// empty or unset, slash-separated. "" declares a store that is NOT a
	// directory under $HOME (an OS keychain): it can be shared in place but
	// never presented anywhere else.
	HomeRel string
	// ReadOnly declares that the run only reads the store.
	ReadOnly bool
}

// HostDir is the store's directory on the host whose home is hostHome:
// Value when the launching env names one, else hostHome/HomeRel. "" for a
// store that is not a directory (HomeRel "" and no Value).
func (s SharedStore) HostDir(hostHome string) string {
	switch {
	case s.Var != "" && s.Value != "":
		return s.Value
	case s.HomeRel == "":
		return ""
	}
	return filepath.Join(hostHome, filepath.FromSlash(s.HomeRel))
}

// Auth is an engine's authentication capability. The engine owns every
// engine-specific fact: which modes it supports, which vars carry each, the
// precedence between them, and how a credential is minted. Callers state the
// mode and hand over the launching env and the store; they never name a var.
type Auth interface {
	// Modes are the auth modes the engine supports.
	Modes() []AuthMode
	// Credentials is what a run in mode needs: it SETS the mode's
	// credential (a value the launching env exports for that mode wins over
	// the stored one), UNSETS every other credential the engine would read
	// ahead of it or instead of it, and declares the human's stores the mode
	// shares. It returns ErrNoCredential (wrapped) when the mode needs a
	// credential that neither shell nor stored holds. It knows nothing of
	// where the run executes.
	Credentials(mode AuthMode, shell func(string) (string, bool), stored CredentialReader) (Credentials, error)
	// Mint runs the engine's own interactive flow on term and returns the
	// credential it produced, never storing it. ErrMintUnsupported for a
	// mode the engine cannot mint.
	Mint(ctx context.Context, mode AuthMode, term Terminal) ([]byte, error)
}

// SupportsMode reports whether a lists mode among its Modes.
func SupportsMode(a Auth, mode AuthMode) bool {
	return slices.Contains(a.Modes(), mode)
}

// validateAuth refuses an Auth that names no mode or a mode outside the
// shared vocabulary: a binding could never select it.
func validateAuth(a Auth) error {
	if a == nil {
		return errors.New("Auth is provided but nil")
	}
	modes := a.Modes()
	if len(modes) == 0 {
		return errors.New("Auth declares no mode")
	}
	for _, m := range modes {
		if _, err := ParseAuthMode(string(m)); err != nil || m == "" {
			return fmt.Errorf("Auth declares mode %q, which is not in the shared vocabulary (%s)", m, strings.Join(AuthModeNames(), ", "))
		}
	}
	return nil
}
