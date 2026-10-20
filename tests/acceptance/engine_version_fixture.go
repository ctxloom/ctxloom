//go:build acceptance

// The engine version a seeded session claims to have run under: a session
// with no recorded version is refused by every transcript reader, so a
// fixture that means to be READ seeds the pinned one.
package acceptance

import (
	"os"
	"path/filepath"
	"strings"

	mockreader "github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader/mock"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// j001000SeededEngineVersion is the engine version a seeded session claims to have
// run under. It is required, not decorative: reader selection is (engine,
// RECORDED version) -> adapter, and a session with no engine_version REFUSES
// to be read at all (vendorreader.SelectAdapter) — which is the correct
// production behaviour and would make every conversion scenario here fail for
// the wrong reason. The values are .github/engine-versions.env's pins, so a
// seeded session claims exactly the version ctxloom's readers are validated
// against. An unrecognised backend deliberately gets no version, so a
// scenario that WANTS the refusal can still ask for one.
//
// Each backend's version is DERIVED from wherever that backend declares it,
// never re-typed here — a hand-copied table agrees with its source on the day
// it is written and silently disagrees forever after, and a seeded session
// would then claim a version no reader is validated against while the
// scenario still passed. The two sources differ because the backends differ:
// a real engine's pin lives in .github/engine-versions.env, the lock CI
// drift-checks against the vendor's published feed; mock has no vendor and no
// feed, so its adapter's own ValidatedVersion is the authority.
func j001000SeededEngineVersion(backend string) string {
	switch backend {
	case "claude-code":
		return enginePinFromLock("CLAUDE_CODE_CLI_VERSION")
	case string(mock.Name):
		if len(mockreader.VersionedAdapters) == 0 {
			return ""
		}
		return mockreader.VersionedAdapters[0].ValidatedVersion
	default:
		return ""
	}
}

// enginePinFromLock reads one KEY=value out of .github/engine-versions.env,
// the tested-version lock. Returns "" when the key is absent, which seeds a
// session with no version and therefore a refusal — loud, and never a guess.
func enginePinFromLock(key string) string {
	repoRoot, err := sourcedir.RepoRoot()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github", "engine-versions.env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, found := strings.Cut(line, "="); found && k == key {
			return v
		}
	}
	return ""
}
