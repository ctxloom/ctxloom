package config

// CurrentConfigVersion is the config format generation this build reads and
// writes: the integer every writer stamps as schema_version. The reader gates
// each layer against it (configload's configKind, whose derived Current a
// test pins to this). Distinct from the application version.
const CurrentConfigVersion = 6
