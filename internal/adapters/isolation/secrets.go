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
	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// secretsTarget is where a container cell's secret files are mounted,
// read-only: one file per secret variable, named by the variable. A
// container path, so slash-separated whatever the host.
const secretsTarget = "/run/ctxloom/secrets"

// secretVars are the credential variables whose VALUE is the secret: every
// variable the mode sets except its FileVars, whose value is a path that
// relocateFiles already rewrote. A cloud mode's switches and region ride
// with its keys: classifying each variable would be a second list of the
// engine's vars to keep true, and a non-secret one costs nothing here.
func secretVars(c engine.Credentials) []string {
	var out []string
	for k := range c.Env {
		if !slices.Contains(c.FileVars, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// containerPlacement is placementOf for a container cell, with each secret
// variable moved out of Env into SecretFiles: its value never rides the
// launch, because the container's runner may dial home over a LAN-visible
// cleartext listener (present.Listen.Public). Container.environment writes
// the files this names.
func containerPlacement(paths present.Paths, l layout, storeEnv map[string]string) launch.Placement {
	pl := placementOf(paths, l, storeEnv)
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
// session's ephemeral dir that holds the run's scratch root. Never the
// scratch root itself: it is new per run, so a crashed run's secret there
// would have no later sibling to reap it. The shared-filesystem probe covers
// either, as it covers every mount.
func secretParent(getenv func(string) string, scratchRoot string) string {
	if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return filepath.Dir(scratchRoot)
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
