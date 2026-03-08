//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// shutdownSignals returns the signals to listen for graceful shutdown.
var shutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
