// Package shellenv resolves the host's default shell from the environment. It
// lives in its own package (depended on by app today, by engine adapters later)
// so the dependency never forms an app↔engine cycle.
package shellenv

import (
	"path"
	"strings"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// ShellFromPath maps a shell executable path (e.g. the value of $SHELL,
// "/usr/bin/zsh") to a known Shell, or "" if it isn't one we parse.
//
// Both separators are honoured on every host. ltk parses cmd.exe and
// PowerShell, so `C:\Windows\System32\cmd.exe` is ordinary input here, and
// filepath.Base follows the HOST: on a POSIX build it does not split on `\`,
// so the whole path became the name and matched nothing. Answering "" there is
// the fail-open direction — the caller then guesses the dialect instead of
// being told it, and a wrapper's inner command is re-parsed in the wrong shell.
// Normalize to slashes and use the slash-only path.Base, as
// frontend.argvBase and rules.shellForProgram already do.
func ShellFromPath(shellPath string) ir.Shell {
	name := strings.ToLower(path.Base(strings.ReplaceAll(strings.TrimSpace(shellPath), `\`, "/")))
	name = strings.TrimSuffix(name, ".exe") // so cmd.exe / pwsh.exe resolve too
	switch name {
	case "bash":
		return ir.ShellBash
	case "zsh":
		return ir.ShellZsh
	case "sh", "dash", "ash", "busybox":
		return ir.ShellSh
	case "mksh", "loksh", "oksh", "pdksh":
		return ir.ShellMksh
	// AT&T ksh93 — what `ksh` is on macOS, RHEL and most commercial unices —
	// is not mksh. It has process substitution and C-style `for ((;;))`; mksh
	// has neither, and the parser's MirBSDKorn variant REJECTS both. Under the
	// shipped on_parse_error: allow a rejection means no rule ever sees the
	// command, so a denied program inside one of those constructs runs while
	// ltk reports a clean allow. There is no ksh93 dialect to choose, and of
	// the two that exist bash accepts a strict superset of what mksh does:
	// across a twelve-construct sweep the two agreed everywhere mksh parsed at
	// all, and differed only where mksh refused. Route ksh to the superset, so
	// the failure mode is a dialect mismatch rather than a silent bypass.
	case "ksh", "ksh93":
		return ir.ShellBash
	case "pwsh", "powershell":
		return ir.ShellPwsh
	case "cmd":
		return ir.ShellCmd
	default:
		return ""
	}
}
