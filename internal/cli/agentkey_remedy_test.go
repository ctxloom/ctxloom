package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
)

// Key-discovery failures are the one moment a user is stuck, so the remedy
// agentkey prints is the whole value of the error. A remedy naming a command
// that does not exist is worse than no remedy: it costs the user a round trip
// and teaches them the tool is unreliable. agentkey cannot check this itself —
// it is below internal/cli and must not import it — so the invariant is
// enforced from here, where both the error messages and the real command tree
// are visible.

// suggestedCtxloomInvocations returns the token list following each bare
// "ctxloom" word in msg. A "ctxloom:" message prefix is deliberately not a
// match: only an invocation the user is meant to TYPE counts.
func suggestedCtxloomInvocations(msg string) [][]string {
	var out [][]string
	for _, line := range strings.Split(msg, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "ctxloom" && i+1 < len(fields) {
				out = append(out, fields[i+1:])
			}
		}
	}
	return out
}

// resolveSuggested walks tokens down the command tree for as long as each one
// names a real subcommand, and returns the deepest command reached plus the
// first token that was not one (an argument, placeholder or flag).
func resolveSuggested(root *cobra.Command, tokens []string) (*cobra.Command, []string) {
	cur := root
	i := 0
	for i < len(tokens) {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == tokens[i] {
				next = c
				break
			}
			for _, a := range c.Aliases {
				if a == tokens[i] {
					next = c
					break
				}
			}
		}
		if next == nil {
			break
		}
		cur = next
		i++
	}
	return cur, tokens[i:]
}

// TestAgentKeyRemedies_NameRealCommands pins that every `ctxloom …` command an
// agentkey error tells the user to run resolves to a real, RUNNABLE command in
// the actual command tree — not to a command group, and not to nothing.
//
// The defect this pins (U135-F15): NoKeyError's last line read
// "To publish unsigned anyway: ctxloom fragment push my-frag --no-sign".
// `ctxloom fragment` has no `push` subcommand at all — publishing is
// `ctxloom bundle push <bundle>` — so the one escape hatch offered to a user
// who cannot sign was an unknown-command error.
func TestAgentKeyRemedies_NameRealCommands(t *testing.T) {
	messages := map[string]string{
		"NoKeyError": (&agentkey.NoKeyError{
			Looked: []string{"git config user.signingkey", "ssh-agent identities"},
		}).Error(),
	}

	for name, msg := range messages {
		invocations := suggestedCtxloomInvocations(msg)
		if len(invocations) == 0 {
			t.Errorf("%s: message suggests no ctxloom command at all:\n%s", name, msg)
			continue
		}
		for _, tokens := range invocations {
			cmd, rest := resolveSuggested(rootCmd, tokens)
			if cmd == rootCmd {
				t.Errorf("%s: suggests `ctxloom %s`, but %q is not a ctxloom command",
					name, strings.Join(tokens, " "), tokens[0])
				continue
			}
			if cmd.Runnable() {
				continue
			}
			t.Errorf("%s: suggests `ctxloom %s`, but `ctxloom %s` is a command GROUP, not something a user can run — %q is not one of its subcommands",
				name, strings.Join(tokens, " "), cmd.CommandPath(), strings.Join(rest, " "))
		}
	}
}
