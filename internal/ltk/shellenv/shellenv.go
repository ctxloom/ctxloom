// Package shellenv resolves the host's default shell from the environment. It
// lives in its own package (depended on by app today, by engine adapters later)
// so the dependency never forms an app↔engine cycle.
package shellenv

import (
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// ShellFromPath maps a shell executable path (e.g. the value of $SHELL,
// "/usr/bin/zsh") to a known Shell, or "" if it isn't one we parse.
func ShellFromPath(shellPath string) ir.Shell {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(shellPath)))
	name = strings.TrimSuffix(name, ".exe") // so cmd.exe / pwsh.exe resolve too
	switch name {
	case "bash":
		return ir.ShellBash
	case "zsh":
		return ir.ShellZsh
	case "sh", "dash", "ash", "busybox":
		return ir.ShellSh
	case "ksh", "ksh93", "mksh", "loksh", "oksh", "pdksh":
		return ir.ShellMksh
	case "pwsh", "powershell":
		return ir.ShellPwsh
	case "cmd":
		return ir.ShellCmd
	default:
		return ""
	}
}

// FromEnv resolves the default shell from a $SHELL value, returning "" when
// it is empty or unrecognized.
func FromEnv(shellValue string) ir.Shell {
	if shellValue == "" {
		return ""
	}
	return ShellFromPath(shellValue)
}
