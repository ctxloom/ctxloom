package config

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// LimitKind says where a per-container resource cap's value comes from.
type LimitKind int

const (
	// LimitDefault: the key is unset, so the isolation adapter derives the
	// cap (from the container daemon's host). Config carries no number for
	// it on purpose: the right value depends on the machine the container
	// runs on, which only the adapter that asks the runtime can see.
	LimitDefault LimitKind = iota
	// LimitUnlimited: the user switched the cap off (`0` or `unlimited`).
	LimitUnlimited
	// LimitFixed: an explicit value (Value).
	LimitFixed
)

// ContainerLimit is one per-container resource cap as configured. Value is
// meaningful only for LimitFixed: bytes for memory, a process count for pids.
type ContainerLimit struct {
	Kind  LimitKind
	Value int64
}

// ContainerLimits are the caps every agent container runs under
// (isolation_memory_limit, isolation_pids_limit). The zero value is "both
// unset": the adapter's defaults.
type ContainerLimits struct {
	Memory ContainerLimit
	PIDs   ContainerLimit
}

// MinContainerMemoryBytes is the smallest memory cap a container runtime
// accepts: docker refuses `--memory` below 6 MB ("Minimum memory limit
// allowed is 6MB"), so a smaller configured cap is refused at load rather
// than at every launch.
const MinContainerMemoryBytes = 6 << 20

// limitUnlimited is the spelling that switches a cap off, beside `0`.
const limitUnlimited = "unlimited"

// ErrInvalidContainerLimit refuses an isolation_memory_limit or
// isolation_pids_limit that does not parse.
var ErrInvalidContainerLimit = errors.New("invalid container resource limit")

// memorySize is docker's size grammar for --memory: a number, optionally
// decimal, then an optional binary unit (k/m/g/t, 1024-based) with an
// optional b / ib suffix, or a bare b. Lower-cased before matching.
var memorySize = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(?:([kmgt])(?:i?b)?|b)?$`)

// memoryUnitShift is each unit's power-of-two exponent.
var memoryUnitShift = map[string]uint{"": 0, "k": 10, "m": 20, "g": 30, "t": 40}

// parseMemoryLimit reads isolation_memory_limit: "" is unset, `unlimited` or
// any zero size is off, otherwise a size of at least MinContainerMemoryBytes.
func parseMemoryLimit(raw string) (ContainerLimit, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "":
		return ContainerLimit{}, nil
	case limitUnlimited:
		return ContainerLimit{Kind: LimitUnlimited}, nil
	}
	m := memorySize.FindStringSubmatch(s)
	if m == nil {
		return ContainerLimit{}, fmt.Errorf("%w: isolation_memory_limit %q is not a size (e.g. 8g, 512m) or %q", ErrInvalidContainerLimit, raw, limitUnlimited)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return ContainerLimit{}, fmt.Errorf("%w: isolation_memory_limit %q: %v", ErrInvalidContainerLimit, raw, err)
	}
	bytes := n * float64(uint64(1)<<memoryUnitShift[m[2]])
	switch {
	case bytes == 0:
		return ContainerLimit{Kind: LimitUnlimited}, nil
	case bytes >= math.MaxInt64:
		return ContainerLimit{}, fmt.Errorf("%w: isolation_memory_limit %q is too large", ErrInvalidContainerLimit, raw)
	case bytes < MinContainerMemoryBytes:
		return ContainerLimit{}, fmt.Errorf("%w: isolation_memory_limit %q is below the 6m container runtimes accept (use 0 or %q to switch the cap off)", ErrInvalidContainerLimit, raw, limitUnlimited)
	}
	return ContainerLimit{Kind: LimitFixed, Value: int64(bytes)}, nil
}

// parsePidsLimit reads isolation_pids_limit: "" is unset, `unlimited` or any
// spelling of zero (`0`, `00`, `+0`) is off, otherwise a positive whole
// process count.
func parsePidsLimit(raw string) (ContainerLimit, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "":
		return ContainerLimit{}, nil
	case limitUnlimited:
		return ContainerLimit{Kind: LimitUnlimited}, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return ContainerLimit{}, fmt.Errorf("%w: isolation_pids_limit %q is not a process count or %q", ErrInvalidContainerLimit, raw, limitUnlimited)
	}
	if n == 0 {
		return ContainerLimit{Kind: LimitUnlimited}, nil
	}
	return ContainerLimit{Kind: LimitFixed, Value: n}, nil
}

// validateIsolationLimits refuses a limit that does not parse, at load.
func validateIsolationLimits(memory, pids string) error {
	if _, err := parseMemoryLimit(memory); err != nil {
		return err
	}
	_, err := parsePidsLimit(pids)
	return err
}

// IsolationLimits returns the configured per-container resource caps. Both
// were validated at load; a Fixture-built Config that carries a malformed
// value reads it as unset, the same as the key being absent.
func (c *Config) IsolationLimits() ContainerLimits {
	memory, _ := parseMemoryLimit(c.isolationMemoryLimit)
	pids, _ := parsePidsLimit(c.isolationPidsLimit)
	return ContainerLimits{Memory: memory, PIDs: pids}
}
