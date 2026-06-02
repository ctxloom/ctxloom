package cmd

import "os"

// reviewBypassEnvVar opts the MCP server out of leaving bundle changes pending.
// Set to "1" in non-interactive runs (CI, cron) where no human will review:
// pending changes are auto-merged into active at startup with a stderr warning.
const reviewBypassEnvVar = "CTXLOOM_AUTO_APPROVE_BUNDLES"

// reviewBypassed returns true when the bypass env var is set to "1".
func reviewBypassed() bool {
	return os.Getenv(reviewBypassEnvVar) == "1"
}
