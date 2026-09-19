// Package cliversion is the cross-binary version contract for the ctxloom
// family. Every binary (ctxloom, ltk, taskloom, …) exposes
// `<binary> version --format json` as {"name","version"}; ctxloom probes its
// companions by parsing exactly this shape, so both the struct AND the probe
// that reads it live here as the single source of truth rather than being
// re-declared per caller.
//
// There are two production readers — boot-time companion discovery
// (internal/core/config.ProbeCompanions) and the agent image's version key
// (internal/adapters/isolation.companionVersionKey) — and one probe. A second
// implementation would let the two disagree about what a companion's version
// IS, which is exactly the drift the image key exists to catch.
package cliversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Info is the machine-readable shape of `<binary> version --format json`.
type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ProbeTimeout bounds the `<bin> version --format json` exec. A wedged
// companion must degrade to an error, never a stalled caller.
//
// ProbeWaitDelay bounds how long Output keeps waiting for the stdout pipe to
// close after the direct child is dead — without it, a companion that spawned
// a grandchild inheriting stdout would stall forever despite the context kill.
//
// Vars (not consts) so tests can shrink them.
var (
	ProbeTimeout   = 3 * time.Second
	ProbeWaitDelay = time.Second
)

// Output runs one binary's version probe and returns its raw stdout. It is a
// var so tests can stand in for the exec; there is no production writer.
var Output = func(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version", "--format", "json")
	cmd.WaitDelay = ProbeWaitDelay
	return cmd.Output()
}

// SetOutputForTesting replaces the exec seam and returns a restore function.
func SetOutputForTesting(fn func(string) ([]byte, error)) func() {
	prev := Output
	Output = fn
	return func() { Output = prev }
}

// Parse extracts the version from a version probe's raw output. A binary that
// answers without a version field is an ERROR, not an empty version: the
// callers key on this value, and "" is indistinguishable from "not probed".
func Parse(raw []byte) (string, error) {
	var info Info
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", fmt.Errorf("parse version --format json output: %w", err)
	}
	if info.Version == "" {
		return "", errors.New("version --format json output has no version field")
	}
	return info.Version, nil
}

// Probe runs the version probe at path and returns the reported version. It
// is the whole probe — exec, then decode — and the only one: internal/core/config's
// boot-time companion discovery and internal/adapters/isolation's agent-image
// version key both read a companion's version through THIS function, so the
// two cannot disagree about what that version is.
func Probe(path string) (string, error) {
	raw, err := Output(path)
	if err != nil {
		return "", fmt.Errorf("run version --format json: %w", err)
	}
	return Parse(raw)
}
