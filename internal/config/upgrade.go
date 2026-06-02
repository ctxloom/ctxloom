package config

import "github.com/ctxloom/ctxloom/internal/upgrade"

// configUpgrades is the canonical, ordered set of config schema upgrades,
// oldest-first. Append an Upgrader here as the schema evolves and bump
// CurrentConfigVersion to match. The loader runs this pipeline over the raw
// config bytes on every load (see loadConfigFile); upgrades apply in memory and
// are only persisted after the user confirms (see Config.CommitUpgrade).
var configUpgrades = upgrade.Pipeline{
	llmRenameUpgrade{},
}
