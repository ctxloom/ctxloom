package linux

import (
	"context"
	"os/exec"
)

// notifySend is the freedesktop notification client (libnotify's).
const notifySend = "notify-send"

// Notify shows title and body with notify-send, when resolve finds it. Its
// output goes nowhere: the human's terminal is not its to write on.
func (OS) Notify(ctx context.Context, resolve func(string) (string, error), title, body string) error {
	bin, err := resolve(notifySend)
	if err != nil {
		return nil
	}
	// "--" ends the options: a title or body that starts with "-" is text.
	return exec.CommandContext(ctx, bin, "--app-name=ctxloom", "--", title, body).Run()
}
