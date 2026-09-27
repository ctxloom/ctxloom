package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
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
)

// AuthModeNames lists the accepted `auth` values, for flag help, shell
// completion and error messages.
func AuthModeNames() []string {
	return []string{string(AuthLogin), string(AuthToken), string(AuthAPIKey)}
}

// ParseAuthMode turns a binding's declared `auth` into its effective mode.
// Undeclared is AuthToken: the default never reaches the human's own login,
// which a binding selects by name. An unknown spelling is refused rather
// than defaulted, because it would silently pick a credential nobody chose.
func ParseAuthMode(declared string) (AuthMode, error) {
	switch m := AuthMode(strings.TrimSpace(declared)); m {
	case "":
		return AuthToken, nil
	case AuthLogin, AuthToken, AuthAPIKey:
		return m, nil
	default:
		return "", fmt.Errorf("auth %q: unknown mode (known: %s)", declared, strings.Join(AuthModeNames(), ", "))
	}
}

// Stored reports whether ctxloom keeps a credential for the mode. The
// human's login is theirs, held where their own engine keeps it; every
// other mode's credential is ctxloom's to store.
func (m AuthMode) Stored() bool { return m != AuthLogin }

var (
	// ErrNoCredential: the mode needs a credential and neither the launching
	// env nor the store holds one. Returned (wrapped) by Auth.LaunchEnv and
	// by a CredentialReader.
	ErrNoCredential = errors.New("no credential for this auth mode")
	// ErrMintUnsupported: the engine cannot mint a credential for the mode;
	// the human supplies one instead.
	ErrMintUnsupported = errors.New("the engine cannot mint a credential for this auth mode")
	// ErrAuthModeUnsupported: the engine does not authenticate in the mode.
	ErrAuthModeUnsupported = errors.New("the engine does not support this auth mode")
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

// Auth is an engine's authentication capability. The engine owns every
// engine-specific fact: which modes it supports, which vars carry each, the
// precedence between them, and how a credential is minted. Callers state the
// mode and hand over the launching env and the store; they never name a var.
type Auth interface {
	// Modes are the auth modes the engine supports.
	Modes() []AuthMode
	// LaunchEnv is the env a run in mode is launched with: it SETS the
	// mode's credential (a value the launching env exports for that mode
	// wins over the stored one) and BLANKS every other credential the engine
	// would otherwise read ahead of it. It returns ErrNoCredential (wrapped)
	// when the mode needs a credential that neither shell nor stored holds.
	LaunchEnv(mode AuthMode, shell func(string) (string, bool), stored CredentialReader) (map[string]string, error)
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
