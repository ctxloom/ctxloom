package agents

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// EnvHost is the host environment a HOST-runtime engine inherits, as the
// binding's `env_host:` and `env:` keys declare it (Agent.HostEnv). The keys
// are spelt as podman spells `--env-host` and `-e NAME`. The zero value
// (env_host absent or true) inherits every variable, minus what the auth mode
// unsets — the engine and every MCP server, hook and shell it starts then see
// every secret the human exported. Curated (env_host: false) starts the engine
// from curatedBase plus Env instead, the same narrowing a container gets from
// naming its `-e` variables. A credential the auth mode needs is not inherited
// at all: the launch sets it by value, so neither list has to name it.
type EnvHost struct {
	Curated bool     `json:"curated,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// ErrEnvWithEnvHost refuses env names with env_host left on: every variable
// is inherited already, and the list would be silently ignored.
var ErrEnvWithEnvHost = errors.New("env: names pass through only with env_host: false")

// ErrEnvNotBareName refuses an env entry that is not a bare variable name.
// A bare name passes the host's value through; setting a value is not what
// this key does.
var ErrEnvNotBareName = errors.New("env: only bare variable names are accepted (NAME, not NAME=value)")

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
func (h EnvHost) Inherits(name string) bool {
	if !h.Curated {
		return true
	}
	if slices.ContainsFunc(curatedBase, func(k string) bool { return envNameEqual(k, name) }) ||
		slices.ContainsFunc(h.Env, func(k string) bool { return envNameEqual(k, name) }) {
		return true
	}
	return slices.ContainsFunc(curatedPrefixes, func(p string) bool {
		return len(name) > len(p) && envNameEqual(p, name[:len(p)])
	})
}

// Validate refuses a declaration that would not do what it says.
func (h EnvHost) Validate() error {
	for _, name := range h.Env {
		if name == "" || strings.Contains(name, "=") {
			return fmt.Errorf("%w: %q", ErrEnvNotBareName, name)
		}
	}
	if !h.Curated && len(h.Env) > 0 {
		return ErrEnvWithEnvHost
	}
	return nil
}

func envNameEqual(a, b string) bool {
	if envNamesFoldCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}
