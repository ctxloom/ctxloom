//go:build windows

package cmd

import (
	"os"
)

// shutdownSignals returns the signals to listen for graceful shutdown.
// On Windows, only os.Interrupt (Ctrl+C) is available.
var shutdownSignals = []os.Signal{os.Interrupt}
