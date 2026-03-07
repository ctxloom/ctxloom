package main

import (
	"os"

	"go.uber.org/zap"

	"github.com/benjaminabbitt/scm/cmd"
)

func main() {
	// Initialize logging (verbose mode if SCM_VERBOSE=1)
	var logger *zap.Logger
	if os.Getenv("SCM_VERBOSE") == "1" {
		logger, _ = zap.NewDevelopment()
	} else {
		cfg := zap.NewProductionConfig()
		cfg.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
		logger, _ = cfg.Build()
	}
	zap.ReplaceGlobals(logger)
	defer func() { _ = logger.Sync() }()

	cmd.Execute()
}
