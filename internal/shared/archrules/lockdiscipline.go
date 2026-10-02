package archrules

// LockDisciplineAllowed is the lock-discipline gate's reasoned, symbol-keyed
// allowlist, in the same "file.go#Symbol" shape as WriteDisciplineAllowed:
// each entry is an unlocked read-modify-write the scan reports, mapped to
// why it stands. The analyzer fails an entry the scan no longer reports.
var LockDisciplineAllowed = map[string]string{}
