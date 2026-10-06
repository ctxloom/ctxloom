package isolation

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/platform"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// secretsTarget is where a container cell's secret dir is mounted,
// read-only. A container path, so slash-separated whatever the host.
const secretsTarget = "/run/ctxloom/secrets"

// secretsFileName is the run's ONE secrets file in its secret dir: every
// secret the run's processes read, as dotenv (sessions.EncodeSecrets).
const secretsFileName = "run.env"

// secretVars are the credential variables a container cell receives as
// secret files: every variable the mode sets, sorted.
func secretVars(c engine.Credentials) []string {
	out := slices.Collect(maps.Keys(c.Env))
	slices.Sort(out)
	return out
}

// containerPlacement is placementOf for a container cell, with each secret
// variable moved out of Env into SecretFiles: its value never rides the
// launch, because the container's runner may dial home over a LAN-visible
// cleartext listener (present.Listen.Public). Container.environment writes
// the files this names.
func containerPlacement(paths present.Paths, l layout) launch.Placement {
	pl := placementOf(paths, l, nil)
	vars := secretVars(l.creds)
	if len(vars) == 0 {
		return pl
	}
	pl.Env = maps.Clone(pl.Env)
	pl.SecretFiles = make(map[string]string, len(vars))
	for _, v := range vars {
		delete(pl.Env, v)
		pl.SecretFiles[v] = path.Join(secretsTarget, secretsFileName)
	}
	return pl
}

// secretScratchPrefix names a run's secret dir (newOwnedScratch).
const secretScratchPrefix = "ctxloom-secret-"

// errSecretUnstaged: a secret must be written but the run made no secrets
// file to hold it.
var errSecretUnstaged = errors.New("run secrets: a secret must be written but the run has no secrets file")

// errCredInExecEnv refuses a runner exec env that still carries the
// coordinator credential: it must reach the runner through the run's secrets
// file (stageCoordCred), never an environment a same-uid process can read.
var errCredInExecEnv = errors.New("run secrets: the coordinator credential is in a runner's exec env instead of its secrets file")

// refuseCredInExecEnv is errCredInExecEnv's check.
func refuseCredInExecEnv(env map[string]string) error {
	if _, ok := env[sessions.EnvCoordCred]; ok {
		return errCredInExecEnv
	}
	return nil
}

// secretsFile is a run's one owner-only secrets file and the values it holds.
// Each put rewrites the whole file, atomically, from every value so far, so
// the file is always a complete, decodable dotenv file.
type secretsFile struct {
	mu      sync.Mutex
	scratch *ownedScratch
	values  map[string]string
}

// newSecretsFile makes a run's secret dir — on the platform's per-user tmpfs,
// else under diskParent, announced once (secretParent) — held by its owner's
// lock, so a crashed run's dir is reaped by the next one made under the same
// parent.
func newSecretsFile(diskParent string) (*secretsFile, error) {
	parent, onDisk := secretParent(os.Getenv, diskParent)
	if onDisk {
		clidiag.WarnOnce("ctxloom", "%s", SecretsOnDiskNotice(parent))
	}
	scratch, err := newOwnedScratch(parent, secretScratchPrefix)
	if err != nil {
		return nil, fmt.Errorf("run secrets: %w", err)
	}
	return &secretsFile{scratch: scratch, values: map[string]string{}}, nil
}

// path is the secrets file's host path.
func (f *secretsFile) path() string { return filepath.Join(f.scratch.dir, secretsFileName) }

// put adds vals to the file. A value the format cannot hold exactly is
// refused (sessions.ErrSecretNotRoundTrippable) and the file is left as it
// was.
func (f *secretsFile) put(vals map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	merged := maps.Clone(f.values)
	maps.Copy(merged, vals)
	b, err := sessions.EncodeSecrets(merged)
	if err != nil {
		return err
	}
	if err := safefs.WriteFile(afero.NewOsFs(), f.path(), b, safefs.PrivateFileMode); err != nil {
		return fmt.Errorf("run secrets: write %s: %w", f.path(), err)
	}
	f.values = merged
	return nil
}

// ErrSecretsOwned refuses to take over a secrets file whose owner still
// holds it: only a dead owner's file is a restarted coordinator's to rewrite.
var ErrSecretsOwned = errors.New("run secrets: the secrets file's owner still holds it")

// RefreshSecrets takes over the secrets file at file — a run's, left behind
// by the coordinator process that launched it and has since ended — and
// rewrites vals into it, keeping every other entry, atomically and
// owner-only. It is the SAME file, in the same dir, that a container mounts
// read-only, so the container's runner reads the new values at its next turn
// (a dir recreated at the path would not be what the container sees).
// Holding the dir's owner lock keeps any later prepare from sweeping it as a
// dead owner's; release, once the run is over, removes the dir and lets go.
func RefreshSecrets(file string, vals map[string]string) (release func(), err error) {
	dir := filepath.Dir(file)
	lock, err := safefs.New().Locks.TryLock(doneContext(), filepath.Join(dir, ownedScratchLockName))
	if errors.Is(err, safefs.ErrLockHeld) {
		return nil, fmt.Errorf("%w: %s", ErrSecretsOwned, dir)
	}
	if err != nil {
		return nil, fmt.Errorf("run secrets: take over %s: %w", dir, err)
	}
	if err := rewriteSecrets(file, vals); err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	return func() {
		// Removed while holding the lock, as reapDeadScratch does.
		_ = os.RemoveAll(dir)
		_ = lock.Unlock()
		_ = os.RemoveAll(dir)
	}, nil
}

// rewriteSecrets lays vals over the dotenv secrets file at file.
func rewriteSecrets(file string, vals map[string]string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("run secrets: read %s: %w", file, err)
	}
	merged, err := sessions.DecodeSecrets(b)
	if err != nil {
		return fmt.Errorf("run secrets: decode %s: %w", file, err)
	}
	maps.Copy(merged, vals)
	if b, err = sessions.EncodeSecrets(merged); err != nil {
		return err
	}
	if err := safefs.WriteFile(afero.NewOsFs(), file, b, safefs.PrivateFileMode); err != nil {
		return fmt.Errorf("run secrets: write %s: %w", file, err)
	}
	return nil
}

// release removes the secret dir, reporting errSecretResidue when it
// survives.
func (f *secretsFile) release() error {
	f.scratch.release()
	if _, err := os.Lstat(f.scratch.dir); !errors.Is(err, fs.ErrNotExist) {
		warnCleanupResidue("run secrets", f.scratch.dir, errSecretResidue)
		return fmt.Errorf("remove run secrets %s: %w", f.scratch.dir, errSecretResidue)
	}
	return nil
}

// secretParent is where a run's secret dir is made: the platform's per-user
// tmpfs when it offers one (platform.PrivateTmpfs), so the value never
// reaches a disk, else diskParent — on disk, which the caller announces
// (SecretsOnDiskNotice). For a container run diskParent is the session's
// scratch dir that holds the run's scratch root, never the scratch root
// itself: it is new per run, so a crashed run's secret there would have no
// later sibling to reap it.
func secretParent(getenv func(string) string, diskParent string) (dir string, onDisk bool) {
	if dir, ok := hostOS.PrivateTmpfs(getenv); ok {
		return dir, false
	}
	return diskParent, true
}

// SecretsOnDiskNotice is the once-per-process announcement that a run's
// secrets are written to disk because the platform offers no per-user tmpfs,
// naming the platform and dir. The doctor reports the same text.
func SecretsOnDiskNotice(dir string) string {
	return fmt.Sprintf("run secrets: %s offers no per-user tmpfs here, so each run's secrets are written owner-only to disk under %s and removed when the run ends", platform.Name, dir)
}

// stageCoordCred moves the coordinator credential out of a runner's spawn
// env into the run's secrets file f, and names the file instead
// (sessions.EnvCoordCredFile) as credFile — the path the RUNNER opens it at.
// The value is then in no exec environment, only in an owner-only file; the
// runner reads it back (sessions.DecodeReach). An env without a credential
// passes through.
func stageCoordCred(f *secretsFile, spawnEnv map[string]string, credFile string) (map[string]string, error) {
	cred, ok := spawnEnv[sessions.EnvCoordCred]
	if !ok {
		return spawnEnv, nil
	}
	if f == nil {
		return nil, errSecretUnstaged
	}
	if err := f.put(map[string]string{sessions.EnvCoordCred: cred}); err != nil {
		return nil, err
	}
	out := maps.Clone(spawnEnv)
	delete(out, sessions.EnvCoordCred)
	out[sessions.EnvCoordCredFile] = credFile
	return out, nil
}

// materializeSecrets writes each secret variable pl names, from creds, into
// the run's secrets file — the host side of the read-only mount at
// secretsTarget.
func materializeSecrets(f *secretsFile, pl launch.Placement, creds engine.Credentials) error {
	vals := make(map[string]string, len(pl.SecretFiles))
	for v := range pl.SecretFiles {
		value, ok := creds.Env[v]
		if !ok {
			return fmt.Errorf("run secrets: the placement names %s, which the run's credentials do not set", v)
		}
		vals[v] = value
	}
	return f.put(vals)
}

// errSecretResidue: teardown could not remove a secret dir.
var errSecretResidue = errors.New("the secret dir is still present")
