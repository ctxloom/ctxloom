//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// shutdownSignals returns the signals to listen for graceful shutdown.
// SIGHUP is included for cases where the parent terminal closes.
var shutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}
