// simple is a generator wrapper that runs any CLI command and outputs
// the result as a context fragment.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/benjaminabbitt/scm/internal/markdown"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: scm-gen-simple <command> [args...]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Runs the specified command and wraps stdout in a context fragment.")
		fmt.Fprintln(os.Stderr, "SECURITY NOTE: this runs *arbitrary* commands.  It will *not* save you from shooting yourself in the foot.  It runs at the same access level as the invoker, so it's not a meaningful security risk, but be aware.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  scm-gen-simple date")
		fmt.Fprintln(os.Stderr, "  scm-gen-simple ls -la")
		fmt.Fprintln(os.Stderr, "  scm-gen-simple cat README.md")
		os.Exit(1)
	}

	command := os.Args[1]
	args := os.Args[2:]

	output, err := runCommand(command, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "command failed: %v\n", err)
		os.Exit(1)
	}

	doc := buildFragment(command, args, output)
	fmt.Print(doc)
}

func runCommand(command string, args []string) (string, error) {
	cmd := exec.Command(command, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("%v: %s", err, stderr.String())
		}
		return "", err
	}

	return stdout.String(), nil
}

func buildFragment(command string, args []string, output string) string {
	frag := markdown.NewFragment()

	// Build a description of what was run
	fullCmd := command
	if len(args) > 0 {
		fullCmd += " " + strings.Join(args, " ")
	}

	ctx := frag.Context()
	ctx.P(fmt.Sprintf("Output of `%s`:", fullCmd))

	// Determine if output looks like it needs a code block
	output = strings.TrimSpace(output)
	if output != "" {
		ctx.CodeBlock("", output)
	} else {
		ctx.P("(no output)")
	}

	// Set variables
	frag.SetVar("command", command)
	frag.SetVar("full_command", fullCmd)

	return frag.String()
}
