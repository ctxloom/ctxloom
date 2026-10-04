package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Interactive console primitives shared by every command whose purpose is to
// ask the user something (the trust and review menus). They belong to no
// single command.

// stdinReader is the single buffered reader over os.Stdin shared by every
// interactive prompt. A fresh bufio.Reader per prompt would silently discard
// any bytes a previous reader buffered past its line (type-ahead / paste between
// back-to-back confirmations), so all prompts read through this one reader.
var stdinReader = bufio.NewReader(os.Stdin)

// promptLine writes prompt to stderr and reads one trimmed line from the shared
// stdin reader, returning the read error (e.g. EOF) so callers can apply their
// own fallback. It is the single read primitive every interactive prompt funnels
// through (the TR4 trust menus) so a line buffered past one
// prompt is not discarded before the next (ctxloom-code-08-002).
func promptLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := stdinReader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// plural picks the singular or plural form for n, for prompts and summaries
// that count things.
func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
