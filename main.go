package main

import (
	"mlcm/cmd"
	"mlcm/internal/logging"
)

func main() {
	// Initialize logging (verbose mode if MLCM_VERBOSE=1)
	_ = logging.Init(logging.IsVerbose())
	defer logging.Sync()

	cmd.Execute()
}
