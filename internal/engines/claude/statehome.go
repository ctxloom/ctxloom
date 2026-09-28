package claude

// HomeLeaf is claude's declared session-home leaf: the home var's Subdir,
// and so the last element of the session home launch.SessionHome places
// (<session>/home/claude), which CLAUDE_CONFIG_DIR names. It is ONE constant
// on purpose: the descriptor declares it, and the rule that places the home
// reads it from there, so the directory the instance config is written into
// and the directory the engine is pointed at are the same by construction.
const HomeLeaf = "claude"
