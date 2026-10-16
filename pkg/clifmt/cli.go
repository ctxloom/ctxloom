package clifmt

import (
	"errors"
	"fmt"
	"strings"
)

// ResolveFormat picks the effective format for one invocation. requested is
// the raw --format value; explicit says whether the user actually gave it;
// terminal says whether the output stream is a terminal; offTerminal is the
// format an unrequested invocation gets off a terminal, with the zero Format
// meaning json.
//
// A format the user did not ask for follows the terminal: text for a human
// watching it, offTerminal (json unless the program configured otherwise)
// for a pipe, a redirect or another process, so a script parsing the output
// fails loudly on a format change instead of misparsing a human rendering
// that was never a stable contract. An explicit request always wins, in both
// directions; an explicit "" is text. Anything else is ParseFormat's answer.
// An offTerminal that names no format is refused rather than replaced by
// json: a program that configured one meant it.
//
// It takes the terminal answer as a bool, so a caller decides how to ask
// (golang.org/x/term, a test override) and this package needs neither.
func ResolveFormat(requested string, explicit, terminal bool, offTerminal Format) (Format, error) {
	switch {
	case !explicit && terminal:
		return FormatText, nil
	case !explicit && offTerminal == "":
		return FormatJSON, nil
	case !explicit:
		if !offTerminal.Valid() {
			return "", UnsupportedFormatError(string(offTerminal))
		}
		return offTerminal, nil
	case requested == "":
		return FormatText, nil
	default:
		return ParseFormat(requested)
	}
}

// FormatUsage is the one help string for a --format flag: the supported
// formats, in order, and what an unset flag does (see ResolveFormat).
func FormatUsage() string { return FormatUsageWith("") }

// FormatUsageWith is FormatUsage for a program whose off-terminal default is
// configurable: offTerminalSource names where it comes from (a config key,
// say), and the help says json applies only when that is unset. An empty
// source is FormatUsage's text.
func FormatUsageWith(offTerminalSource string) string {
	names := FormatNames()
	list := strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
	if offTerminalSource == "" {
		return "Output format: " + list + " (default: text on a terminal, json when output is piped or redirected)"
	}
	return "Output format: " + list + " (default: text on a terminal; when output is piped or redirected, " + offTerminalSource + ", or json when that is unset)"
}

// ExitCoder is an error that names the process exit status it should end
// with. ExitCodeOf reads it off an error chain the way RemedyOf reads a fix.
type ExitCoder interface{ ExitCode() int }

// ExitCodeOf is the exit status for err: 0 for nil, the first ExitCoder's
// code in errors.As order (wrapped and joined chains alike, outermost
// first), and 1 for any other error.
func ExitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ec ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

// ExitStatus is an error meaning "exit with Code and report nothing": the
// command has already said all it has to say, or relays the status of a
// process it wrapped, which is that process's outcome rather than a failure
// of the CLI's own.
type ExitStatus struct{ Code int }

func (e ExitStatus) Error() string { return fmt.Sprintf("exit code %d", e.Code) }

// ExitCode returns Code.
func (e ExitStatus) ExitCode() int { return e.Code }
