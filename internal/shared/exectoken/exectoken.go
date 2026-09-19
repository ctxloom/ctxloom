// Package exectoken identifies which tool wrote a shell command by the
// command's executable token — path-, quote-, and verb-agnostic — so a
// callback's subcommand can drift without orphaning the old form, and no
// in-file marker is needed (some engines' strict settings schemas forbid one).
package exectoken

import "strings"

// IsManaged reports whether command was written by the tool whose executable
// basename is bin. Each family tool owns its own executable namespace, which
// is why exec-token identity suffices. bin is required — a tool reconciles
// only its OWN entries, never "any family tool's", so it must not touch a
// sibling's hooks.
func IsManaged(command, bin string) bool {
	return bin != "" && Token(command) == bin
}

// Token returns the executable basename of command: the first shell word,
// unquoted, without its directory or a Windows .exe suffix.
func Token(command string) string {
	exe := firstToken(command)
	if i := strings.LastIndexAny(exe, `/\`); i >= 0 {
		exe = exe[i+1:]
	}
	exe = strings.TrimSuffix(exe, ".exe")
	return strings.Trim(exe, `"'`)
}

func firstToken(command string) string {
	command = strings.TrimLeft(command, " \t")
	if command == "" {
		return ""
	}
	if q := command[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(command[1:], q); end >= 0 {
			return command[1 : 1+end]
		}
		return command[1:] // unterminated quote: take the remainder
	}
	if i := strings.IndexAny(command, " \t"); i >= 0 {
		return command[:i]
	}
	return command
}
