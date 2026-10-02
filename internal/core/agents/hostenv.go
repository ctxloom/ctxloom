package agents

import (
	"errors"
	"slices"
	"strings"
)

// HostEnv is a binding's `host_env:` block: which of the launching
// environment's variables a HOST-runtime engine inherits. Undeclared (the
// zero value) inherits all of them, minus what the auth mode unsets — the
// engine and every MCP server, hook and shell it starts then see every
// secret the human exported. Curated starts the engine from curatedBase plus
// Passthrough instead, the same narrowing a container gets from naming its
// `-e` variables. A credential the auth mode needs is not inherited at all:
// the launch sets it by value, so neither list has to name it.
type HostEnv struct {
	Curated     bool     `yaml:"curated,omitempty" json:"curated,omitempty"`
	Passthrough []string `yaml:"passthrough,omitempty" json:"passthrough,omitempty"`
}

// ErrHostEnvPassthroughUncurated refuses a passthrough list with no opt-in:
// with nothing curated, every variable is inherited and the list would be
// silently ignored.
var ErrHostEnvPassthroughUncurated = errors.New("host_env: passthrough applies only with curated: true")

// curatedBase is what a curated engine keeps by exact name: what a process
// needs to find binaries, its home and its user, render a terminal and
// pick a locale and temp dir — and, on Windows, the variables the OS itself
// needs for a child process to start.
var curatedBase = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "COLORTERM", "LANG", "TMPDIR",
	"SystemRoot", "SystemDrive", "windir", "ComSpec", "PATHEXT", "USERPROFILE", "USERNAME",
	"APPDATA", "LOCALAPPDATA", "ProgramData", "ProgramFiles", "TEMP", "TMP", "HOMEDRIVE", "HOMEPATH",
}

// curatedPrefixes are the families a curated engine keeps: the locale, the
// XDG base dirs, and ctxloom's own carriers the launch relies on.
var curatedPrefixes = []string{"LC_", "XDG_", "CTXLOOM_"}

// Inherits reports whether an engine under h keeps the variable name from
// the environment it is launched from. Names compare case-insensitively on
// Windows, where the environment does.
func (h HostEnv) Inherits(name string) bool {
	if !h.Curated {
		return true
	}
	if slices.ContainsFunc(curatedBase, func(k string) bool { return envNameEqual(k, name) }) ||
		slices.ContainsFunc(h.Passthrough, func(k string) bool { return envNameEqual(k, name) }) {
		return true
	}
	return slices.ContainsFunc(curatedPrefixes, func(p string) bool {
		return len(name) > len(p) && envNameEqual(p, name[:len(p)])
	})
}

// Validate refuses a declaration that would not do what it says.
func (h HostEnv) Validate() error {
	if !h.Curated && len(h.Passthrough) > 0 {
		return ErrHostEnvPassthroughUncurated
	}
	return nil
}

func envNameEqual(a, b string) bool {
	if envNamesFoldCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}
