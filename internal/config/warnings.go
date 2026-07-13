package config

// WarningKind classifies a warning collected during config load, so startup
// choke owners can gate on the class (fail-loudly) instead of string-matching
// warning text. All four kinds are fatal-class in strict mode — a
// present-but-broken config or a lossy migration must abort startup — while an
// ABSENT config file stays fine and produces no warning at all.
type WarningKind string

const (
	// WarnKindRead: the config file exists but could not be read (EACCES, a
	// directory in its place, transient I/O).
	WarnKindRead WarningKind = "read"
	// WarnKindParse: the config file's YAML failed to unmarshal.
	WarnKindParse WarningKind = "parse"
	// WarnKindValidate: the config parsed but failed schema validation.
	WarnKindValidate WarningKind = "validate"
	// WarnKindUnknownKey: the config carries a key ctxloom does not know — a
	// typo, or a key RETIRED by a schema generation the migrator has already
	// passed (the classic: `profiles.defaults` copied out of a stale doc into a
	// current-version config). Split out of WarnKindValidate so the key can be
	// named, the near-miss suggested, and the retired key's replacement offered,
	// instead of the raw jsonschema pointer reaching the user. Silently dropping
	// it is the worst outcome: the setting looks applied and is not.
	WarnKindUnknownKey WarningKind = "unknown-key"
	// WarnKindMigrationLossy: the in-memory schema upgrade had to drop a
	// user-set value (e.g. a compaction model with no label to attach it to).
	WarnKindMigrationLossy WarningKind = "migration-lossy"
)

// Warning is one non-fatal load-time diagnostic: the degradation text plus the
// kind the strictness gate keys on.
type Warning struct {
	Kind WarningKind
	Text string
}
