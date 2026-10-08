// Package opencodeengine holds the adapters the composition root hands the
// opencode kind: the reading of `opencode --version`, which is the version
// adapter's parse (adapters/engineversion), and the reader of the bridge
// plugin's transcripts (transcripts.go), a VendorAdapter, so the kind itself
// never imports an adapter.
package opencodeengine

import (
	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Version is `opencode --version`, which prints the bare version; Floor is
// the pinned version ctxloom's driver was validated against
// (.github/engine-versions.env OPENCODE_CLI_VERSION).
func Version() engine.VersionCommand { return engineversion.LeadingToken("1.18.35") }
