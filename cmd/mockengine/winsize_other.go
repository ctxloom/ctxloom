//go:build !unix

package main

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// resizeNotifications has no SIGWINCH to subscribe to here; a nil channel
// never fires, so an interactive session simply never reports a resize.
func resizeNotifications(*os.File) <-chan agent.WindowSize { return nil }
