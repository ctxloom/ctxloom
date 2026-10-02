package isolation

import (
	"maps"
	"path"
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
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
