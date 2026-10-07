package archrules

// LedgerDisciplineAllowed is the ledger-discipline gate's reasoned,
// symbol-keyed allowlist: a durable "file.go#Symbol" reference (Symbol is
// "Type.Method" for a method, the bare function name otherwise) mapped to
// why the entry stands. Each entry is a heuristic blind spot the gate's
// scan cannot see past, or a real gap deferred with its reason; the
// analyzer fails an entry the scan no longer reports.
var LedgerDisciplineAllowed = map[string]string{
	"internal/core/agent/commandfiles.go#WriteManagedCommandFiles":            declaredByTheCaller,
	"internal/core/agent/managed_skill_packages.go#WriteManagedSkillPackages": declaredByTheCaller,
	"internal/engines/claude/commandfiles.go#writeCommandDir":                 declaredByTheCaller,
}

// declaredByTheCaller is why the managed-tree writers stand: each returns the
// host path of every file it placed, the calling approach DECLARES exactly
// those (present.Delivered.Files), and the static writer's ownership record
// (fsstatic.Records) owns them from that declaration. The record is the
// caller's to reference, so the writer itself spells no ownership signal.
const declaredByTheCaller = "the ownership record is the caller's declaration: this writer returns the paths it placed, the approach declares them as present.Delivered.Files, and fsstatic.Records owns them; the writer spells no ownership signal itself"
