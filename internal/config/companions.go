package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/resources"
)

// companionProbeTimeout bounds the `<bin> version --format json` exec at
// boot. A wedged companion must degrade to a warning, never a stalled
// startup. companionProbeWaitDelay bounds how long Output keeps waiting for
// the stdout pipe to close after the direct child is dead — without it, a
// companion that spawned a grandchild inheriting stdout would stall startup
// forever despite the context kill. Vars (not consts) so tests can shrink them.
var (
	companionProbeTimeout   = 3 * time.Second
	companionProbeWaitDelay = time.Second
)

// companionVersionOutput runs a companion's version probe; seam for tests.
var companionVersionOutput = func(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), companionProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version", "--format", "json")
	cmd.WaitDelay = companionProbeWaitDelay
	return cmd.Output()
}

// SetCompanionVersionOutputForTesting overrides the version-probe exec seam
// and returns a restore function. Companion of SetLookPathForTesting.
func SetCompanionVersionOutputForTesting(fn func(string) ([]byte, error)) func() {
	prev := companionVersionOutput
	companionVersionOutput = fn
	return func() { companionVersionOutput = prev }
}

// CompanionStatus is one boot-time probe of a companion binary — a standalone
// tool (taskloom, ltk) whose built-in bundle wires it into the agent session.
type CompanionStatus struct {
	Bin     string
	Path    string // resolved PATH location; empty when not installed
	Version string // self-reported via `<bin> version --format json`
	Err     error  // version-probe failure for a present binary
}

// BuiltinCompanionBins returns the unique non-ctxloom executables referenced
// by the embedded built-in bundles' hooks and MCP servers, sorted. Deriving
// the set from the YAML (rather than a hardcoded list) means a new built-in
// bundle's companion is probed at boot automatically.
func BuiltinCompanionBins() []string {
	seen := map[string]bool{}
	names, err := resources.ListBuiltinBundles()
	if err != nil {
		clidiag.Warn("ctxloom", "list builtin bundles: %v", err)
		return nil
	}
	for _, name := range names {
		data, err := resources.GetBuiltinBundle(name)
		if err != nil {
			clidiag.Warn("ctxloom", "read builtin bundle %q: %v", name, err)
			continue
		}
		var b bundles.Bundle
		if err := yaml.Unmarshal(data, &b); err != nil {
			clidiag.Warn("ctxloom", "parse builtin bundle %q: %v", name, err)
			continue
		}
		for _, hs := range [][]bundles.BundleHook{
			b.Hooks.PreTool, b.Hooks.PostTool, b.Hooks.SessionStart,
			b.Hooks.SessionEnd, b.Hooks.PreShell, b.Hooks.PostFileEdit,
		} {
			for _, h := range hs {
				if bin := companionBin(h.Command); bin != "" {
					seen[bin] = true
				}
			}
		}
		for _, m := range b.MCP {
			if bin := companionBin(m.Command); bin != "" {
				seen[bin] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for bin := range seen {
		out = append(out, bin)
	}
	sort.Strings(out)
	return out
}

// ProbeCompanions resolves each built-in companion on PATH and asks it for
// its version. Missing binaries yield Path == "" (their bundle entries are
// skipped by the resolvers, which also emit the install hint); a present
// binary whose probe fails carries the error. Reporting only — never fatal.
//
// Probes run concurrently: each is bounded by companionProbeTimeout, so a
// sequential loop would add that bound per wedged companion to startup. Running
// them in parallel keeps the worst-case wall-clock to ~one timeout (CLAUDE.md:
// never block startup). Output order is preserved (sorted by bin) since each
// goroutine writes its own slot.
func ProbeCompanions() []CompanionStatus {
	bins := BuiltinCompanionBins()
	out := make([]CompanionStatus, len(bins))
	var wg sync.WaitGroup
	for i, bin := range bins {
		wg.Add(1)
		go func(i int, bin string) {
			defer wg.Done()
			st := CompanionStatus{Bin: bin}
			if path, err := lookPath(bin); err == nil {
				st.Path = path
				st.Version, st.Err = companionVersion(path)
			}
			out[i] = st
		}(i, bin)
	}
	wg.Wait()
	return out
}

// companionVersion runs `<path> version --format json` and extracts the version.
func companionVersion(path string) (string, error) {
	raw, err := companionVersionOutput(path)
	if err != nil {
		return "", fmt.Errorf("run version --format json: %w", err)
	}
	var info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", fmt.Errorf("parse version --format json output: %w", err)
	}
	if info.Version == "" {
		return "", errors.New("version --format json output has no version field")
	}
	return info.Version, nil
}
