package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
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
		fmt.Fprintf(os.Stderr, "ctxloom: warning: list builtin bundles: %v\n", err)
		return nil
	}
	for _, name := range names {
		data, err := resources.GetBuiltinBundle(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: read builtin bundle %q: %v\n", name, err)
			continue
		}
		var b bundles.Bundle
		if err := yaml.Unmarshal(data, &b); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: parse builtin bundle %q: %v\n", name, err)
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
func ProbeCompanions() []CompanionStatus {
	var out []CompanionStatus
	for _, bin := range BuiltinCompanionBins() {
		st := CompanionStatus{Bin: bin}
		if path, err := lookPath(bin); err == nil {
			st.Path = path
			st.Version, st.Err = companionVersion(path)
		}
		out = append(out, st)
	}
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
