package main

import "path/filepath"

// progName is the binary/command name, used in the cobra root and as the prefix
// on stderr diagnostics.
const progName = "ltk"

// The .ltk layout. These are the single source of truth for where config and
// runtime state live, so `manage install` (which writes them) and `evaluate`
// (which reads them) can never drift apart.
const (
	configDir    = ".ltk"        // project-local ltk directory
	configBase   = "config.yaml" // rules file inside configDir
	stateBase    = "state.json"  // confirm-by-repeating override state inside configDir
	legacyConfig = ".ltk.yaml"   // flat config path kept for back-compat
)

// defaultConfigPath is the rules file `manage install` writes and `evaluate`
// prefers: ".ltk/config.yaml".
var defaultConfigPath = filepath.Join(configDir, configBase)
