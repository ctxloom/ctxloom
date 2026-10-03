package isolation

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
	"github.com/ctxloom/ctxloom/internal/shared/platform"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// secretsTarget is where a container cell's secret files are mounted,
// read-only: one file per secret variable, named by the variable. A
// container path, so slash-separated whatever the host.
const secretsTarget = "/run/ctxloom/secrets"

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
		pl.SecretFiles[v] = path.Join(secretsTarget, v)
	}
	return pl
}

// secretScratchPrefix names a container cell's secret dir (newOwnedScratch).
const secretScratchPrefix = "ctxloom-secret-"

// errSecretUnstaged: a Placement names a secret file but the workspace made
// no secret dir to hold it.
var errSecretUnstaged = errors.New("container secrets: the placement names a secret file but the workspace has no secret dir")

// secretParent is where a container cell's secret dir is made: the user's
// runtime dir when the session has one — a tmpfs the XDG spec makes
// owner-only, so the value never reaches a disk — else (macOS, Windows) the
// session's scratch dir that holds the run's scratch root, on disk. Never the
// scratch root itself: it is new per run, so a crashed run's secret there
// would have no later sibling to reap it. The shared-filesystem probe covers
// either, as it covers every mount. onDisk reports the fallback, which the
// caller announces (secretsOnDiskNotice).
func secretParent(getenv func(string) string, scratchRoot string) (dir string, onDisk bool) {
	if dir := getenv(runtimeDirEnv); dir != "" {
		return dir, false
	}
	return filepath.Dir(scratchRoot), true
}

// runtimeDirEnv names the user's per-session tmpfs (XDG base dirs).
const runtimeDirEnv = "XDG_RUNTIME_DIR"

// secretsOnDiskNotice is the once-per-process announcement that a container
// run's secrets are written to disk because the platform offers no per-user
// tmpfs. The doctor reports the same fact (operations' secrets-storage check).
func secretsOnDiskNotice(dir string) string {
	return fmt.Sprintf("container secrets: %s has no per-user tmpfs ($%s is unset), so each container run's secrets are written owner-only to disk under %s and removed when the run ends", platform.Name, runtimeDirEnv, dir)
}

// stageCoordCred moves the coordinator credential out of a container
// runner's spawn env into the run's secret dir, and names the file instead
// (sessions.EnvCoordCredFile): the value is then in neither the `run`
// client's environment nor the container's, only in an owner-only file the
// container sees read-only. The runner reads it back byte for byte
// (sessions.DecodeReach). An env without a credential passes through.
func stageCoordCred(cw *containerWorkspace, spawnEnv map[string]string) (map[string]string, error) {
	cred, ok := spawnEnv[sessions.EnvCoordCred]
	if !ok {
		return spawnEnv, nil
	}
	if cw.secrets == nil {
		return nil, errSecretUnstaged
	}
	if err := safefs.WriteFile(afero.NewOsFs(), filepath.Join(cw.secrets.dir, sessions.EnvCoordCred), []byte(cred), owneronly.FileMode); err != nil {
		return nil, fmt.Errorf("container secrets: write %s: %w", sessions.EnvCoordCred, err)
	}
	out := maps.Clone(spawnEnv)
	delete(out, sessions.EnvCoordCred)
	out[sessions.EnvCoordCredFile] = path.Join(secretsTarget, sessions.EnvCoordCred)
	return out, nil
}

// materializeSecrets writes each secret variable pl names, from creds, as an
// owner-only file named by the variable in dir — the host side of the
// read-only mount at secretsTarget. The exact value is written: the runner
// reads it back byte for byte.
func materializeSecrets(dir string, pl launch.Placement, creds engine.Credentials) error {
	for v := range pl.SecretFiles {
		value, ok := creds.Env[v]
		if !ok {
			return fmt.Errorf("container secrets: the placement names %s, which the run's credentials do not set", v)
		}
		if err := safefs.WriteFile(afero.NewOsFs(), filepath.Join(dir, v), []byte(value), owneronly.FileMode); err != nil {
			return fmt.Errorf("container secrets: write %s: %w", v, err)
		}
	}
	return nil
}

// errSecretResidue: teardown could not remove a secret dir.
var errSecretResidue = errors.New("the secret dir is still present")
