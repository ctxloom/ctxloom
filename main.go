package main

import (
	"github.com/benjaminabbitt/mlcm/cmd"
	"github.com/benjaminabbitt/mlcm/internal/logging"
)

func main() {
	// Initialize logging (verbose mode if MLCM_VERBOSE=1)
	_ = logging.Init(logging.IsVerbose())
	defer logging.Sync()

	cmd.Execute()
}
