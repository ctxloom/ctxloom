package main

import (
	"os"

	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/cli"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

func main() {
	// Degraded mode from the environment, read BEFORE dispatch so the
	// pre-cobra window (config discovery, projectroot) already runs in the
	// right mode. CTXLOOM_DEGRADED=1 is the hook/generated-registration
	// mechanism (e.g. an MCP registration that must serve despite a broken
	// project); the persistent --degraded flag wins over it once parsed (see
	// cli root's PersistentPreRun). Deliberately NO config key: a broken
	// config cannot excuse itself.
	if os.Getenv("CTXLOOM_DEGRADED") == "1" {
		strictness.SetDegraded(true)
	}

	// Companion discovery off from the environment, read BEFORE dispatch for the
	// same reason: the pre-cobra window can already assemble context, and probing
	// EXECUTES the companion binaries on PATH. CTXLOOM_NO_COMPANIONS=1 is the
	// mechanism for a subprocess/CI that must not depend on what the host has
	// installed; the persistent --no-companions flag wins over it once parsed
	// (see cli root's PersistentPreRun).
	if os.Getenv("CTXLOOM_NO_COMPANIONS") == "1" {
		config.SetCompanionsDisabled(true)
	}

	// Initialize logging (verbose mode if CTXLOOM_VERBOSE=1)
	var logger *zap.Logger
	if os.Getenv("CTXLOOM_VERBOSE") == "1" {
		logger, _ = zap.NewDevelopment()
	} else {
		cfg := zap.NewProductionConfig()
		cfg.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
		logger, _ = cfg.Build()
	}
	zap.ReplaceGlobals(logger)
	defer func() { _ = logger.Sync() }()

	cli.Execute()
}
