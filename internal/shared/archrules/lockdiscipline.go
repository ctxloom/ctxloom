package archrules

// LockDisciplineAllowed is the lock-discipline gate's reasoned, symbol-keyed
// allowlist, in the same "file.go#Symbol" shape as WriteDisciplineAllowed:
// each entry is an unlocked read-modify-write the scan reports, mapped to
// why it stands. Both runners fail an entry the scan no longer reports.
var LockDisciplineAllowed = map[string]string{
	"internal/core/agent/managedcontext.go#writeManagedContextLocked": "false positive (leaf helper under the caller's lock): writeManagedContextLocked is WriteManagedContext's body, split out for readability and invoked BY NAME from inside WriteManagedContext's own agent.WithFileLock closure (see its doc: \"run under its caller's lock\") — same shape as CodexHookWriter.save above. See this file's header, blind spot 4.",
}
