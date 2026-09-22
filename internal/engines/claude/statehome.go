package claude

// HomeLeaf is the directory INSIDE a ctxloom-provisioned instance home
// (paths.HarpSessionEngineHomes) that CLAUDE_CONFIG_DIR names. It is ONE constant on
// purpose: the engine's descriptor declares it as the home var's Subdir, so
// the seed internal/adapters/isolation writes and the directory the engine is
// pointed at are the same directory by construction, on every cell.
const HomeLeaf = "claude"
