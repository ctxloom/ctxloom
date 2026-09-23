package isolation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// companionPin produces the directory holding the ADMITTED companions — the
// bytes companions.admitCompanion verified and their signatures
// (companions.PinAdmittedCompanions) — or "" when none is admitted. Injected
// by the CLI at startup (SetCompanionPin), as SetBinaryVersion is: admission
// needs the configuration's trust root, which this package does not hold.
// Unset means no companion is admitted.
var companionPin func() (string, error)

// SetCompanionPin injects the admitted-companion directory provider. Called
// once by the CLI at startup; nil clears it.
func SetCompanionPin(fn func() (string, error)) { companionPin = fn }

// errCompanionNotAdmitted: no admitted copy of the named companion exists.
var errCompanionNotAdmitted = errors.New("companion not admitted")

// pinnedCompanionDir asks the provider for the admitted-companion directory.
// A provider fault is reported and treated as "nothing admitted": a launch
// never fails over an auxiliary tool, and nothing unverified is substituted.
func pinnedCompanionDir() string {
	if companionPin == nil {
		return ""
	}
	dir, err := companionPin()
	if err != nil {
		clidiag.Warn("ctxloom", "cannot pin the admitted companions (%v); companion hooks and MCP servers "+
			"resolve through the inherited PATH for this launch", err)
		return ""
	}
	return dir
}

// pinnedCompanionLookPath resolves name to its admitted copy, refusing a
// companion that has none — whatever a PATH lookup would have found.
func pinnedCompanionLookPath(name string) (string, error) {
	dir := pinnedCompanionDir()
	if dir == "" {
		return "", fmt.Errorf("%s: %w", name, errCompanionNotAdmitted)
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s: %w", name, errCompanionNotAdmitted)
	}
	return p, nil
}

// withPinnedPath returns env with the admitted-companion directory put FIRST
// on its PATH.
//
// WHY: a companion's loadout hooks and MCP servers name it by its bare name
// (agent.CtxloomCommand's invariant keeps absolute paths out of settings
// files), so the engine resolves that name through the PATH it inherits from
// the host runner — and companions.admitCompanion verified only the file
// ctxloom's own PATH resolved. An unsigned binary of the same name earlier on
// the inherited PATH would otherwise run as the hook. Leading with the pinned
// directory makes the bare name reach the admitted bytes.
//
// HOST ONLY. A container runner's PATH is the image's, where the staged
// companions lead (stageCompanions, companionPathLeads); a host directory does
// not exist there.
func withPinnedPath(env []string) []string {
	dir := pinnedCompanionDir()
	if dir == "" {
		return env
	}
	inherited, set := "", false
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			inherited, set = v, true // the last assignment is the one os/exec keeps
		}
	}
	path := dir
	if set && inherited != "" {
		path += string(os.PathListSeparator) + inherited
	}
	return append(env, "PATH="+path)
}
