package gitutil

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// configCountEnv is the counter of git's numbered config overrides, and
// configPairPrefixes name the pairs it counts (GIT_CONFIG_KEY_<n>,
// GIT_CONFIG_VALUE_<n>). `git rev-parse --local-env-vars` lists the counter but
// not the pairs, which no fixed list can enumerate, so the pairs are matched by
// prefix wherever the counter is listed. They must travel together: git refuses
// to run at all with a count whose pairs are missing.
const configCountEnv = "GIT_CONFIG_COUNT"

var configPairPrefixes = []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"}

// gitEnvPrefix is what every variable git reads from the environment starts
// with. It is the whole rule when git cannot be asked which of them are local.
const gitEnvPrefix = "GIT_"

// repoLocationEnvVars is git's own list of the variables that are local to the
// repository git is operating on, asked once per process. Nil means git could
// not be asked.
var repoLocationEnvVars = sync.OnceValue(func() []string {
	return resolveLocalEnvVars(probeLocalEnvVars)
})

// resolveLocalEnvVars returns what probe reports, or nil when it cannot answer.
func resolveLocalEnvVars(probe func() ([]string, error)) []string {
	vars, err := probe()
	if err != nil || len(vars) == 0 {
		return nil
	}
	return vars
}

// probeLocalEnvVars asks git which environment variables are local to a
// repository — the set git itself clears when it moves into another one. It is
// the one subprocess this package runs, and it asks nothing about any
// repository: it runs from the filesystem root with an EMPTY environment, so the
// very variables it reports cannot steer the question.
func probeLocalEnvVars() ([]string, error) {
	cmd := exec.Command("git", "rev-parse", "--local-env-vars")
	// The filesystem root (the current drive's, on Windows), outside any repository.
	cmd.Dir = string(filepath.Separator)
	// Empty, not nil: a nil Env inherits os.Environ().
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git rev-parse --local-env-vars: %w", err)
	}
	return strings.Fields(string(out)), nil
}

// IsRepoLocationEnvVar reports whether key is an environment variable that
// changes which repository git operates on, or how git reads it — GIT_DIR and
// its siblings, and the config overrides `git -c` exports to its children
// (GIT_CONFIG_PARAMETERS, GIT_CONFIG_COUNT and the pairs it counts). The set is
// git's own answer to `git rev-parse --local-env-vars`, so it follows the
// installed git rather than a list kept by hand. Any of these inherited by a git
// child process overrides the exec.Cmd's Dir or the repository's own config.
func IsRepoLocationEnvVar(key string) bool {
	return isLocalEnvVar(repoLocationEnvVars(), key)
}

// isLocalEnvVar is IsRepoLocationEnvVar against a given answer from git. With
// no answer — git could not be asked — every GIT_ variable counts: every git
// ctxloom spawns needs that same binary and fails on its own in that state, so
// the sanitiser errs towards stripping rather than towards passing through, and
// keeps no list of its own to drift from git's.
func isLocalEnvVar(vars []string, key string) bool {
	if vars == nil {
		return strings.HasPrefix(key, gitEnvPrefix)
	}
	if slices.Contains(vars, key) {
		return true
	}
	if !slices.Contains(vars, configCountEnv) {
		return false
	}
	for _, prefix := range configPairPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
