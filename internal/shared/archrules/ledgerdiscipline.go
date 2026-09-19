package archrules

// LedgerDisciplineAllowed is the ledger-discipline gate's reasoned,
// symbol-keyed allowlist: a durable "file.go#Symbol" reference (Symbol is
// "Type.Method" for a method, the bare function name otherwise) mapped to
// why the entry stands. Each entry is a heuristic blind spot the gate's
// scan cannot see past, or a real gap deferred with its reason; both
// runners fail an entry the scan no longer reports.
var LedgerDisciplineAllowed = map[string]string{
	"internal/engines/claude/commandfiles.go#WriteCommandFiles":       "false positive, blind spot 1 (helper split across functions): delegates straight to agent.WriteManagedCommandFiles, which delegates to agent.WriteManagedPackageFiles — that is where the ledger.Ledger{...} construction and led.Write/led.Read calls actually live (packagefiles.go). WriteCommandFiles itself never spells any ownership-record signal.",
	"internal/core/agent/managedcontext.go#writeManagedContextLocked": "false positive: the in-file-marker mechanism IS implemented here (ManagedContextBegin/ManagedContextEnd construction, splitManagedSection), which is its own ownership record by design — but this gate's marker-call signal only recognizes the EXPORTED entry points (WriteManagedContext/DeliverManagedContext/StripManagedSection), not the private splitManagedSection helper or the inline marker-constant construction actually used here. Same root cause as lock_discipline_test.go's identical entry for this symbol (blind spot 4: helper split out of the caller's lock/record).",
}
