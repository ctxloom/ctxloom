package isolation

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// A ctxloom-launched engine that shares no login authenticates from ONE
// long-lived token in its env (engine.TokenAuth.TokenVar), filled by
// ExportStoredTokens; a host run of an engine declaring
// engine.HomeSpec.SharedLogin uses the human's own login in place instead,
// with the token blanked. Nothing is copied into a session home, mounted into
// a container or refreshed by ctxloom.
//
// Why: an OAuth refresh token is single-use and rotates. Native sessions stay
// in step only because they share one credentials file AND one lock pair. A
// per-session copy has its own config dir and its own locks, and a container
// bind of the one file pins the inode claude replaces by rename, so copies
// went stale and a refresh from one could revoke the rest. A shared login is
// the ONE file and lock pair; a setup-token is never refreshed and never
// written by the engine. Neither has a second holder to fall out of step with.

var (
	// ErrNoTokenAuth: the engine declares no token var to store a token for.
	ErrNoTokenAuth = errors.New("engine takes no stored token")
	// ErrEmptyToken: nothing but whitespace was supplied.
	ErrEmptyToken = errors.New("no token supplied")
	// ErrMalformedToken: the input holds more than one word, so it is not
	// one token (a pasted command, or two lines).
	ErrMalformedToken = errors.New("input is not a single token")
)

const (
	tokenFileMode fs.FileMode = 0o600
	tokenDirMode  fs.FileMode = 0o700
)

// TokenSource is where an engine's token var got its value in this process.
type TokenSource string

const (
	// TokenSourceEnv: the user exported the var; it wins over the store.
	TokenSourceEnv TokenSource = "env"
	// TokenSourceStored: ExportStoredTokens filled it from the stored file.
	TokenSourceStored TokenSource = "stored"
	// TokenSourceNone: the var is unset.
	TokenSourceNone TokenSource = "none"
)

var (
	tokenSourcesMu sync.Mutex
	// storedExports are the vars ExportStoredTokens set in this process, so
	// a later status can tell them from ones the user exported.
	storedExports = map[string]bool{}
)

// TokenAuthFor is the engine's declared token auth; false when the engine is
// unknown or declares none.
func TokenAuthFor(name string) (engine.TokenAuth, bool) {
	f, ok := factsFor(name)
	if !ok {
		return engine.TokenAuth{}, false
	}
	return f.Home.Auth.Get()
}

// StoreEngineToken writes token as engine's stored token: owner-only from
// creation (iox.WriteFileAtomic), in an owner-only directory, surrounding
// whitespace trimmed. It returns the file's path and never echoes the token.
func StoreEngineToken(name string, token []byte) (string, error) {
	if _, ok := TokenAuthFor(name); !ok {
		return "", fmt.Errorf("%s: %w", name, ErrNoTokenAuth)
	}
	tok := bytes.TrimSpace(token)
	if len(tok) == 0 {
		return "", ErrEmptyToken
	}
	if len(bytes.Fields(tok)) != 1 {
		return "", ErrMalformedToken
	}
	path, err := paths.HomeEngineTokenPath(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), tokenDirMode); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.Chmod(filepath.Dir(path), tokenDirMode); err != nil {
		return "", fmt.Errorf("restrict %s: %w", filepath.Dir(path), err)
	}
	if err := iox.WriteFileAtomic(path, tok, tokenFileMode, iox.Durable()); err != nil {
		return "", err
	}
	return path, nil
}

// ExportStoredTokens sets each engine's token var from its stored token when
// the process env leaves it unset. It runs once at process start, so every
// launch path inherits the var: a container through the name-only
// passthrough (engine.ContainerAuth), a host engine through the runner's env
// unless its cell shares the human's login and blanks it.
func ExportStoredTokens() error {
	var errs []error
	for _, name := range factNames() {
		a, ok := TokenAuthFor(name)
		if !ok || os.Getenv(a.TokenVar) != "" {
			continue
		}
		path, err := paths.HomeEngineTokenPath(name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		raw, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("read the stored %s token: %w", name, err))
			continue
		}
		tok := string(bytes.TrimSpace(raw))
		if tok == "" {
			continue
		}
		if err := os.Setenv(a.TokenVar, tok); err != nil {
			errs = append(errs, err)
			continue
		}
		tokenSourcesMu.Lock()
		storedExports[a.TokenVar] = true
		tokenSourcesMu.Unlock()
	}
	return errors.Join(errs...)
}

// EngineTokenStatus is one engine's token state, without the token.
type EngineTokenStatus struct {
	Engine string
	Var    string
	Path   string
	Stored bool
	Mode   fs.FileMode
	Source TokenSource
}

// EngineTokenStatuses reports every engine that declares token auth.
func EngineTokenStatuses() ([]EngineTokenStatus, error) {
	var out []EngineTokenStatus
	for _, name := range factNames() {
		a, ok := TokenAuthFor(name)
		if !ok {
			continue
		}
		path, err := paths.HomeEngineTokenPath(name)
		if err != nil {
			return nil, err
		}
		st := EngineTokenStatus{Engine: name, Var: a.TokenVar, Path: path, Source: TokenSourceNone}
		if info, err := os.Stat(path); err == nil {
			st.Stored, st.Mode = true, info.Mode().Perm()
		}
		if os.Getenv(a.TokenVar) != "" {
			st.Source = TokenSourceEnv
			tokenSourcesMu.Lock()
			if storedExports[a.TokenVar] {
				st.Source = TokenSourceStored
			}
			tokenSourcesMu.Unlock()
		}
		out = append(out, st)
	}
	return out, nil
}
