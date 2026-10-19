package runner

import (
	"errors"
	"strings"
	"unicode"
)

// A batch engine is run through cmd.exe on a command line built here, not by
// handing the script to CreateProcess: CreateProcess runs a .cmd or .bat
// through `cmd.exe /c` on the caller's command line as it stands, and /c
// strips that line's first and last quote whenever it holds more than one
// pair, so a quoted script path with spaces (an npm shim under
// C:\Users\<first last>) stops naming one file. The builder is plain string
// work, built everywhere so it is tested everywhere; only Windows starts a
// process with it (launcher_windows.go).

// cmdExePrefix starts the line: command extensions on (the percent-sign
// guard below relies on them), delayed expansion off (so `!` is literal),
// no AutoRun commands, and the quote that wraps the whole command for /c to
// strip.
const cmdExePrefix = `cmd.exe /e:ON /v:OFF /d /c "`

// percentGuard is put before each `%` of an argument: `%cd:~,%` is an empty
// substring of a variable that always exists, so cmd.exe expands it to
// nothing and cannot pair the `%` that follows with another into %NAME%.
const percentGuard = `%%cd:~,`

// bareBatchPunct is the ASCII punctuation an argument may hold and still be
// passed unquoted: none of it is cmd.exe syntax. Every other ASCII symbol,
// space and control character makes the argument quoted.
const bareBatchPunct = `#$*+-./:?@\_`

var (
	// errBatchArgument refuses an argument cmd.exe cannot carry: a line
	// break or NUL ends the command there, whatever the quoting.
	errBatchArgument = errors.New("a batch engine's argument cannot hold a line break or NUL: cmd.exe would end the command there")
	// errBatchScriptPath refuses a script path that cannot be quoted as one
	// file name: Windows file names hold no quote and no NUL, and a trailing
	// backslash would escape the closing quote.
	errBatchScriptPath = errors.New("a batch engine's path cannot hold a quote or NUL or end in a backslash")
)

// batchScript reports whether path names a batch script, which CreateProcess
// runs through cmd.exe: a .cmd or .bat extension in any case, after the
// trailing dots and spaces Windows drops from a file name.
func batchScript(path string) bool {
	name := strings.ToLower(strings.TrimRight(path, ". "))
	return strings.HasSuffix(name, ".cmd") || strings.HasSuffix(name, ".bat")
}

// batchCommandLine is the cmd.exe command line that runs the batch script at
// script with args, each argument reaching the script as the one word it is.
func batchCommandLine(script string, args []string) (string, error) {
	if strings.ContainsAny(script, "\"\x00") || strings.HasSuffix(script, `\`) {
		return "", errBatchScriptPath
	}
	var b strings.Builder
	b.WriteString(cmdExePrefix)
	b.WriteString(`"` + script + `"`)
	for _, arg := range args {
		if strings.ContainsAny(arg, "\r\n\x00") {
			return "", errBatchArgument
		}
		b.WriteByte(' ')
		appendBatchArg(&b, arg)
	}
	b.WriteByte('"')
	return b.String(), nil
}

// appendBatchArg writes arg to b as one batch argument. A quoted argument has
// each embedded quote doubled, which both cmd.exe and the C runtime rules a
// script's program parses %* with read as a literal quote; a run of
// backslashes is doubled only where a quote follows it, the C runtime's rule.
func appendBatchArg(b *strings.Builder, arg string) {
	quote := batchArgNeedsQuotes(arg)
	if quote {
		b.WriteByte('"')
	}
	backslashes := 0
	for _, r := range arg {
		switch r {
		case '\\':
			backslashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, backslashes))
			b.WriteByte('"')
			backslashes = 0
		case '%':
			b.WriteString(percentGuard)
			backslashes = 0
		default:
			backslashes = 0
		}
		b.WriteRune(r)
	}
	if quote {
		b.WriteString(strings.Repeat(`\`, backslashes))
		b.WriteByte('"')
	}
}

// batchArgNeedsQuotes reports whether arg must be quoted: it is empty (it
// would vanish), ends in a backslash (a script quoting it as "%~1" would have
// that backslash escape its own quote), or holds a character outside the
// known-bare set.
func batchArgNeedsQuotes(arg string) bool {
	if arg == "" || strings.HasSuffix(arg, `\`) {
		return true
	}
	for _, r := range arg {
		ascii := r <= unicode.MaxASCII
		bare := r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune(bareBatchPunct, r)
		if ascii && !bare || unicode.IsControl(r) {
			return true
		}
	}
	return false
}
