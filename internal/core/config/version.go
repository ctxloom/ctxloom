package config

// CurrentConfigVersion is the config SCHEMA version: the integer Save stamps
// into every config.yaml it writes, and the floor the reader requires — a
// file declaring an older version (or none) is refused with a fatal-class
// finding, never rewritten in place. Distinct from the application version.
const CurrentConfigVersion = 6
