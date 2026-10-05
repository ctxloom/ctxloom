package isolation

import (
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// hostHomeDir is the seam over the host user's home directory: where a
// shared credential store lives (engine.SharedStore.HostDir) and what an
// engine's instance-config writer reads ambient values from. Overridable in
// tests.
var hostHomeDir = os.UserHomeDir

// sharedStore is one declared credential store at stage 1: the declaration
// and the host directory it resolved to ("" for a store that is no
// directory, an OS keychain).
type sharedStore struct {
	engine.SharedStore
	hostDir string
}

// stageStores resolves each store the run's credentials declare to its host
// directory and refuses one that is missing: a login shared in place starts
// logged out when the store is not there, and refusing loudly beats that.
func stageStores(eng string, stores []engine.SharedStore) ([]sharedStore, error) {
	if len(stores) == 0 {
		return nil, nil
	}
	home, err := hostHomeDir()
	if err != nil {
		return nil, fmt.Errorf("credential store: cannot resolve the home directory: %w", err)
	}
	out := make([]sharedStore, 0, len(stores))
	for _, s := range stores {
		st := sharedStore{SharedStore: s, hostDir: s.HostDir(home)}
		if st.hostDir != "" {
			if fi, err := os.Stat(st.hostDir); err != nil || !fi.IsDir() {
				return nil, report.Errorf(
					fmt.Sprintf("sign %s in on this host so %s exists, or set `auth: token` in your config", eng, st.hostDir),
					"%s: the credential store %s this auth mode shares is missing: %w", eng, st.hostDir, engine.ErrNoCredential)
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// CheckStores is stageStores' refusal alone: nil when every credential store
// the run's credentials share in place is on this host, else the same
// refusal (and fix) preparing the run would raise. A launch asks it before
// anything is established for a run that could not start logged in.
func CheckStores(eng string, stores []engine.SharedStore) error {
	_, err := stageStores(eng, stores)
	return err
}

// presentEnvKeys returns the subset of keys that getenv reports as set
// (non-empty), in order: a SCOPED allowlist filter, so the host's full
// environment never blanket-crosses into a container (hostTerminalEnv).
func presentEnvKeys(getenv func(string) string, keys []string) []string {
	var out []string
	for _, k := range keys {
		if getenv(k) != "" {
			out = append(out, k)
		}
	}
	return out
}
