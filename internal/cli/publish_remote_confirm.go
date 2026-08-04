package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// The interactive half of the publish-remote confirmation. The POLICY — what
// is recorded, what counts as confirmed, and what happens when nobody can be
// asked — lives in internal/remote (publish_confirm.go); this file is only the
// terminal, so every non-CLI frontend (MCP, ACP, the VSCode companion) reaches
// the refusal rather than inheriting a prompt it cannot render.

// publishRemoteAsk returns the confirmation callback for a publishing command,
// or NIL when this process has no interactive terminal.
//
// Returning nil rather than a callback that answers "no" is deliberate: nil is
// how package remote recognises "there is nobody to ask" and emits the refusal
// that names the remote and says how to confirm it. A callback that quietly
// returned false would collapse "you declined" and "nobody could be asked"
// into one message, and the second is the one an agent or a CI job needs to
// read.
func publishRemoteAsk(cmd *cobra.Command) remote.PublishRemoteAsk {
	if !isInteractiveTerminal() {
		return nil
	}
	return func(_ context.Context, remoteURL string) (bool, error) {
		return promptPublishRemote(cmd.ErrOrStderr(), remoteURL), nil
	}
}

// promptPublishRemote renders the consequence block and reads the answer.
//
// It is split from publishRemoteAsk — whose only other job is the no-TTY gate
// — because that gate makes the prompt untestable in-process: in any test
// binary isInteractiveTerminal() is false, so a combined function would take
// the skip path every time and the most consequential text on the publishing
// path would have nothing asserting it is shown at all. Same reason it writes
// to the command's stderr rather than os.Stderr; in production they are the
// same descriptor.
func promptPublishRemote(w io.Writer, remoteURL string) bool {
	fmt.Fprintf(w, "\nPublish to %s?\n\n"+
		"  Nothing has been published to this remote before. Your signed content will be\n"+
		"  pushed there with your own git credentials.\n\n"+
		"  Check the URL is the one you meant: a typo publishes somewhere else, and a\n"+
		"  remote can arrive in a .ctxloom/ config that came with a clone.\n\n"+
		"  Answering yes records it; you will not be asked about this remote again.\n\n",
		remoteURL)
	yes, err := promptYesNo("  [y/N] ")
	return err == nil && yes
}
