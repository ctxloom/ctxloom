package isolation

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// An engine's stored credentials: one owner-only file per engine and auth
// mode under the ctxloom home. Nothing here knows which env var carries a
// credential or which mode an engine prefers: the engine's own Auth reads
// what is stored (StoredCredentials) and decides. Nothing is copied into a
// session home, mounted into a container or refreshed by ctxloom.
//
// Why only long-lived credentials are stored: an OAuth refresh token is
// single-use and rotates. Native sessions stay in step only because they
// share one credentials file AND one lock pair; a copy has its own locks,
// went stale, and a refresh from one revoked the rest. A minted token and an
// API key are never refreshed, so a stored one has no second holder to fall
// out of step with. The login is never stored: a run in that mode is pointed
// at the human's own.

var (
	// ErrNoAuth: the engine is unknown or declares no auth to store for.
	ErrNoAuth = errors.New("engine declares no auth")
	// ErrNotStored: the mode keeps no credential ctxloom could store (the
	// human's own login).
	ErrNotStored = errors.New("the auth mode stores no credential")
	// ErrEmptyCredential: nothing but whitespace was supplied.
	ErrEmptyCredential = errors.New("no credential supplied")
	// ErrMalformedCredential: the input holds more than one word, so it is
	// not one credential (a pasted command, or two lines).
	ErrMalformedCredential = errors.New("input is not a single credential")
	// ErrCredentialExposed: the stored credential, or the directory holding
	// it, is open to someone other than its owner, so it is not used.
	ErrCredentialExposed = errors.New("stored credential is not owner-only")
)

const (
	credentialFileMode fs.FileMode = 0o600
	credentialDirMode  fs.FileMode = 0o700
)

// AuthFor is the engine's declared auth; false when the engine is unknown or
// declares none.
func AuthFor(name string) (engine.Auth, bool) {
	f, ok := factsFor(name)
	if !ok {
		return nil, false
	}
	return f.Home.Auth.Get()
}

// storableAuth is the engine's auth when mode is one it supports AND one
// ctxloom stores a credential for.
func storableAuth(name string, mode engine.AuthMode) (engine.Auth, error) {
	f, ok := factsFor(name)
	if !ok {
		return nil, report.Errorf("name a registered engine with --engine", "%s: %w", name, ErrNoAuth)
	}
	if _, err := engine.CheckAuth(engine.Name(name), f.Home.Auth, string(mode)); err != nil {
		return nil, err
	}
	a, ok := f.Home.Auth.Get()
	if !ok {
		return nil, report.Errorf(fmt.Sprintf("%s authenticates on its own; there is nothing to store", name), "%s: %w", name, ErrNoAuth)
	}
	if !mode.Stored() {
		var stored []string
		for _, m := range a.Modes() {
			if m.Stored() {
				stored = append(stored, string(m))
			}
		}
		return nil, report.Errorf(fmt.Sprintf("nothing is stored for auth %s: it uses what your own shell or login already holds; the modes of %s ctxloom stores a credential for are: %s", mode, name, strings.Join(stored, ", ")),
			"%s %s: %w", name, mode, ErrNotStored)
	}
	return a, nil
}

// restrictDir is restrictCredentialDir, indirected so a test can make it
// fail: no ACL a test can write stops an elevated Windows administrator (the
// account CI runs as) from replacing a DACL, so the failure has no honest
// on-disk fixture there.
var restrictDir = restrictCredentialDir

// StoreEngineCredential writes secret as engine's stored credential for
// mode: owner-only from creation (iox.WriteFileAtomic), in an owner-only
// directory, surrounding whitespace trimmed. It returns the file's path and
// never echoes the secret. The result is held to the same check a read
// applies, and a credential that fails it is removed: a store never leaves a
// readable credential.
func StoreEngineCredential(name string, mode engine.AuthMode, secret []byte) (string, error) {
	if _, err := storableAuth(name, mode); err != nil {
		return "", err
	}
	s := bytes.TrimSpace(secret)
	if len(s) == 0 {
		return "", ErrEmptyCredential
	}
	if len(bytes.Fields(s)) != 1 {
		return "", ErrMalformedCredential
	}
	path, err := paths.HomeEngineCredentialPath(name, string(mode))
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, credentialDirMode); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	if err := restrictDir(dir); err != nil {
		return "", fmt.Errorf("restrict %s to its owner: %w", dir, err)
	}
	if err := iox.WriteFileAtomic(path, s, credentialFileMode, iox.Durable()); err != nil {
		return "", err
	}
	if err := checkCredentialPrivate(path); err != nil {
		return "", errors.Join(err, os.Remove(path))
	}
	return path, nil
}

// checkCredentialPrivate refuses a credential whose directory or file is
// open to anyone but its owner, with ErrCredentialExposed naming the path
// and the fix. A missing path is returned as is (fs.ErrNotExist).
func checkCredentialPrivate(path string) error {
	for _, p := range []string{filepath.Dir(path), path} {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		why, err := ownerOnlyViolation(p, info)
		if err != nil {
			return fmt.Errorf("check who may read %s: %w", p, err)
		}
		if why != "" {
			return fmt.Errorf("%w: %s %s; store it again (`ctxloom auth mint` or `ctxloom auth set`) to restrict it", ErrCredentialExposed, p, why)
		}
	}
	return nil
}

// StoredCredentials is engine's credential store as its Auth reads it.
func StoredCredentials(name string) engine.CredentialReader {
	return storedCredentials{engine: name}
}

type storedCredentials struct{ engine string }

// Read returns the stored credential for mode, trimmed. Nothing stored is
// engine.ErrNoCredential; a credential others can read is refused with
// ErrCredentialExposed rather than used or treated as absent.
func (s storedCredentials) Read(mode engine.AuthMode) ([]byte, error) {
	path, err := paths.HomeEngineCredentialPath(s.engine, string(mode))
	if err != nil {
		return nil, err
	}
	err = checkCredentialPrivate(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", path, engine.ErrNoCredential)
	}
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	c := bytes.TrimSpace(raw)
	if len(c) == 0 {
		return nil, fmt.Errorf("%s is empty: %w", path, engine.ErrNoCredential)
	}
	return c, nil
}

// EngineCredentialStatus is one engine's stored credential for one mode,
// without the credential. Protection is this platform's verdict on who can
// read it (describeProtection); empty when nothing is stored.
type EngineCredentialStatus struct {
	Engine     string
	Mode       engine.AuthMode
	Path       string
	Stored     bool
	Protection string
}

// EngineCredentialStatuses reports every stored-credential mode of every
// engine that declares auth, in the engine's own mode order.
func EngineCredentialStatuses() ([]EngineCredentialStatus, error) {
	var out []EngineCredentialStatus
	for _, name := range factNames() {
		a, ok := AuthFor(name)
		if !ok {
			continue
		}
		for _, mode := range slices.DeleteFunc(slices.Clone(a.Modes()), func(m engine.AuthMode) bool { return !m.Stored() }) {
			st, err := credentialStatus(name, mode)
			if err != nil {
				return nil, err
			}
			out = append(out, st)
		}
	}
	return out, nil
}

func credentialStatus(name string, mode engine.AuthMode) (EngineCredentialStatus, error) {
	path, err := paths.HomeEngineCredentialPath(name, string(mode))
	if err != nil {
		return EngineCredentialStatus{}, err
	}
	st := EngineCredentialStatus{Engine: name, Mode: mode, Path: path}
	info, err := os.Stat(path)
	if err != nil {
		return st, nil
	}
	st.Stored = true
	st.Protection, err = describeProtection(path, info)
	if err != nil {
		return EngineCredentialStatus{}, fmt.Errorf("check who may read %s: %w", path, err)
	}
	return st, nil
}

// aclExposure names the grantees of an access list beyond the owner and the
// principals every owner-only ACL on the platform tolerates, "" when there
// are none. It is the platform-neutral half of the Windows owner-only check
// (ownerOnlyViolation there reads the ACL and hands the SIDs here as
// strings), kept out of the build-tagged file so it is tested everywhere.
func aclExposure(owner string, tolerated, grantees []string) string {
	var extra []string
	for _, g := range grantees {
		if g == owner || slices.Contains(tolerated, g) || slices.Contains(extra, g) {
			continue
		}
		extra = append(extra, g)
	}
	if len(extra) == 0 {
		return ""
	}
	return "grants access to " + strings.Join(extra, ", ")
}
