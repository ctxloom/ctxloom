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

// fallbackLocalEnvVars stands in for git's answer only when git cannot be
// asked. Every git ctxloom spawns needs the same binary, so in that state those
// spawns fail on their own; this list exists so the sanitiser still errs
// towards stripping rather than towards passing everything through. It is git
// 2.47's answer.
var fallbackLocalEnvVars = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE",
	"GIT_INDEX_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX", "GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
}

// repoLocationEnvVars is git's own list of the variables that are local to the
// repository git is operating on, asked once per process.
var repoLocationEnvVars = sync.OnceValue(func() []string {
	return resolveLocalEnvVars(probeLocalEnvVars)
})

// resolveLocalEnvVars returns what probe reports, or fallbackLocalEnvVars when
// it cannot answer.
func resolveLocalEnvVars(probe func() ([]string, error)) []string {
	vars, err := probe()
	if err != nil || len(vars) == 0 {
		return fallbackLocalEnvVars
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
	vars := repoLocationEnvVars()
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
