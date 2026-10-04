package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// AuthMode is HOW an engine authenticates, in words every engine shares.
// Which env vars carry each mode and which one the engine reads first are the
// ENGINE's answers (Auth); the modes themselves are vocabulary.
//
// Who runs in which mode is not a choice an agent makes: every run ctxloom
// spawns (a delegated child, a one-shot) authenticates with AuthToken, and
// only the human's own session may share their login (launch.RunAuth).
type AuthMode string

const (
	// AuthLogin: the human's own login, in place: the engine is pointed at
	// the credential the human's own engine already keeps. Only the human's
	// own session runs in it, and only on the host.
	AuthLogin AuthMode = "login"
	// AuthToken: a long-lived token the HUMAN mints with the engine's own
	// flow and exports in the launching env (or their secret manager's);
	// read from there and handed to the engine's env. ctxloom never captures
	// or stores it: Anthropic's terms forbid a third party to collect, store
	// or intermediate Claude.ai credentials.
	AuthToken AuthMode = "token"
)

// AuthModeNames lists the accepted `auth` values, for error messages and
// the config schema.
func AuthModeNames() []string {
	return []string{string(AuthLogin), string(AuthToken)}
}

// ParseAuthMode turns a declared `auth` into its effective mode. Undeclared
// is AuthToken: the default never reaches the human's own login, which is
// selected by name. An unknown spelling is refused rather than defaulted,
// because it would silently pick a credential nobody chose.
func ParseAuthMode(declared string) (AuthMode, error) {
	switch m := AuthMode(strings.TrimSpace(declared)); m {
	case "":
		return AuthToken, nil
	case AuthLogin, AuthToken:
		return m, nil
	default:
		return "", report.Errorf("declare one of "+strings.Join(AuthModeNames(), ", "), "auth %q: %w", declared, ErrUnknownAuthMode)
	}
}

var (
	// ErrNoCredential: the mode needs a credential the launching env does not
	// hold. Returned (wrapped) by Auth.Credentials.
	ErrNoCredential = errors.New("no credential for this auth mode")
	// ErrHostOnlyStore: the mode shares a credential store, and no container
	// is given one, so a container run in it is refused.
	ErrHostOnlyStore = errors.New("the auth mode shares a credential store no container is given")
	// ErrAuthModeUnsupported: the engine does not authenticate in the mode.
	ErrAuthModeUnsupported = errors.New("the engine does not support this auth mode")
	// ErrUnknownAuthMode: the declared mode is not in the shared vocabulary.
	ErrUnknownAuthMode = errors.New("unknown auth mode")
)

// Credentials is what a run in one auth mode needs, as runtime-neutral DATA:
// the environment that prepares the run decides how each piece is made true
// where the engine runs, and the engine never learns which environment that
// was.
type Credentials struct {
	// Mode is the auth mode these credentials are for, stamped where they
	// are resolved: what the run's engine home is prepared for (a login
	// run's instance carries the login's account half; no other does).
	Mode AuthMode
	// Env is laid over the engine's environment: the mode's credential.
	Env map[string]string
	// Unset names variables removed from the engine's environment before it
	// starts. It exists because an empty value is not an absent one: for some
	// variables "" means a real default (claude reads an empty
	// credential-storage var as $HOME/.claude), and some switches are read as
	// set whatever their value.
	Unset []string
	// Stores are the human's own credential stores the mode shares in place
	// (a login's storage). Each must exist where the run starts; no container
	// is given one, so a container run declaring any is refused
	// (ErrHostOnlyStore).
	Stores []SharedStore
}

// SharedStore is one of the human's credential stores a run shares in place
// rather than copies: the engine reads it and writes it (a login refreshes).
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
	// directory under $HOME (an OS keychain).
	HomeRel string
}

// CredentialSource identifies WHERE a run's credential comes from, never what
// it is: the engine, the NAMES of the variables that carry it, and the
// locations of the stores it is read from. No value is read or kept, so a
// source may be journaled and shown; the values' digest is Fingerprint's, kept
// apart from it.
//
// Two runs of one coordinator with equal Keys authenticate as one principal:
// every run's credential is resolved from the coordinator's one launching
// environment, so one carrier there is one credential.
type CredentialSource struct {
	// Key is the comparable identity; "" when the run carries no credential.
	Key string
	// EnvVars name the variables whose value was captured at launch, sorted.
	// A value refreshed afterwards never reaches a run already started.
	EnvVars []string
	// Stores are the stores read in place, sorted: the location the launching
	// env names, else where the engine finds it under $HOME, else (a store
	// that is no directory) the variable that names it.
	Stores []string
}

// Source is where c's credential comes from, for engine eng.
func (c Credentials) Source(eng Name) CredentialSource {
	src := CredentialSource{}
	for k := range c.Env {
		src.EnvVars = append(src.EnvVars, k)
	}
	for _, st := range c.Stores {
		src.Stores = append(src.Stores, st.location())
	}
	if len(src.EnvVars) == 0 && len(src.Stores) == 0 {
		return src
	}
	slices.Sort(src.EnvVars)
	slices.Sort(src.Stores)
	src.Key = fmt.Sprintf("%s env=%s store=%s", eng, strings.Join(src.EnvVars, ","), strings.Join(src.Stores, ","))
	return src
}

// Fingerprint is a one-way digest of the credential values c captured (Env),
// with the variables that carry them: what tells a re-authenticated
// credential from the one an engine refused without keeping either. "" when
// c captured no value — a store read in place is not in hand, and a login
// refresh reaches the runs sharing it without one.
func (c Credentials) Fingerprint() string {
	return EnvFingerprint(slices.Collect(maps.Keys(c.Env)), func(k string) (string, bool) {
		v, ok := c.Env[k]
		return v, ok
	})
}

// EnvFingerprint is Fingerprint over the values lookup gives vars now: what a
// launch from that environment would record. "" when vars is empty or any of
// them is unset, which tells nothing about the credential.
func EnvFingerprint(vars []string, lookup func(string) (string, bool)) string {
	if len(vars) == 0 {
		return ""
	}
	h := sha256.New()
	_, _ = h.Write([]byte("ctxloom credential fingerprint\x00"))
	for _, k := range slices.Sorted(slices.Values(vars)) {
		v, ok := lookup(k)
		if !ok {
			return ""
		}
		// Length-prefixed, so no pair of carriers and values reads as another.
		_, _ = fmt.Fprintf(h, "%d:%s%d:%s", len(k), k, len(v), v)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// location names the store without resolving $HOME: the launching env's
// value, else the engine's default under $HOME, else the variable itself.
func (s SharedStore) location() string {
	switch {
	case s.Value != "":
		return s.Value
	case s.HomeRel != "":
		return "~/" + s.HomeRel
	}
	return s.Var
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
// engine-specific fact: which modes it supports, which vars carry each, and
// the precedence between them. Callers state the mode and hand over the
// launching env; they never name a var.
type Auth interface {
	// Modes are the auth modes the engine supports.
	Modes() []AuthMode
	// Credentials is what a run in mode needs: it SETS the mode's
	// credential from the launching env, UNSETS every other credential the
	// engine would read ahead of it or instead of it, and declares the
	// human's stores the mode shares. It returns ErrNoCredential (wrapped,
	// with a remedy naming how the human supplies one) when the mode needs a
	// credential the shell does not export. It knows nothing of where the
	// run executes.
	Credentials(mode AuthMode, shell func(string) (string, bool)) (Credentials, error)
}

// TokenSetup is declared by an Auth whose AuthToken credential the human
// creates with the engine's OWN CLI. ctxloom runs that flow on the human's
// terminal and reads nothing it prints: the token goes from the engine's flow
// to the human, who exports it. Nothing here hands a token to ctxloom.
type TokenSetup interface {
	// SetupArgs are the arguments to the engine's binary that start its
	// token-creation flow.
	SetupArgs() []string
	// TokenEnv is the variable the human exports the token in.
	TokenEnv() string
}

// SupportsMode reports whether a lists mode among its Modes.
func SupportsMode(a Auth, mode AuthMode) bool {
	return slices.Contains(a.Modes(), mode)
}

// validateAuth refuses an Auth that names no mode, a mode outside the shared
// vocabulary, or no AuthToken: every run ctxloom spawns authenticates with
// the token, so an engine that declares auth and lacks it could run no agent.
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
	if !slices.Contains(modes, AuthToken) {
		return fmt.Errorf("Auth does not declare %q, the mode every agent run authenticates in", AuthToken)
	}
	return nil
}
