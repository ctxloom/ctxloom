package taskstest

// appDirEscapeRatchet lists, by repository-relative package path, the test
// packages that still resolve ctxloom's app directory through an ancestor of
// their own source tree — the escape requireIsolatedAppDir otherwise refuses.
//
// WHY A LIST AND NOT A FLAG DAY. Making Isolate fail closed turns every
// package that has not adopted testsupport.SandboxedMain red in one commit.
// A red integration branch is not a smaller cost than the escape; it is the
// same cost paid by whoever starts work next, plus archaeology. So the check
// lands together with this list, seeded with exactly the packages that were
// escaping when it landed. The escape becomes VISIBLE and BOUNDED on the day
// the check lands, and every removal from this list is a package that can
// never regress.
//
// THE ONLY LEGAL EDIT IS A DELETION. Fix a package by giving it the
// process-wide sandbox, which closes the working-directory route for every
// test in the binary whether or not it remembers to call Isolate:
//
//	func TestMain(m *testing.M) { os.Exit(testsupport.SandboxedMain(m)) }
//
// then delete its line here. Adding a line re-opens the hole that overwrote a
// developer's real ~/.ctxloom/config.yaml; if a new package truly cannot
// adopt the sandbox, say why on its line.
//
// KEYED PER PACKAGE, NOT PER FILE, on purpose. The escape is a property of the
// test BINARY — its working directory is its package's source directory — so
// the package is the unit that can actually be fixed, and a two-dozen-line
// list stays readable where a hundred-and-twenty-line one does not.
//
// TestAppDirEscapeRatchet_IsLive fails on any entry that no longer escapes, so
// the list cannot quietly stop shrinking. A stale exemption is worse than no
// exemption: it reads as a known hazard while covering nothing.
var appDirEscapeRatchet = map[string]bool{
	"cmd/validate":                            true,
	"internal/agentcoord/coord":               true,
	"internal/claude":                         true,
	"internal/config":                         true,
	"internal/lm/backends":                    true,
	"internal/lm/grpc":                        true,
	"internal/lm/isolation":                   true,
	"internal/mcp":                            true,
	"internal/memory":                         true,
	"internal/opencode":                       true,
	"internal/operations":                     true,
	"internal/paths":                          true,
	"internal/projectroot":                    true,
	"internal/remote":                         true,
	"internal/sessions":                       true,
	"internal/shared/agent":                   true,
	"internal/shared/tasks/operations":        true,
	"internal/transcript":                     true,
	"internal/transcript/vendorreader/claude": true,
	"internal/transcript/vendorreader/codex":  true,
	"internal/transcript/vendorreader/kiro":   true,
	"internal/vpio/dockerexec":                true,

	// The two isolation helpers themselves. Their own tests drive Isolate and
	// ProjectDir as SUBJECTS and assert on the working directory they leave
	// behind (ProjectDir's cleanup must restore the cwd it was called from),
	// and repoRoot walks up from that same cwd to find the repository. A
	// process-wide sandbox would move the binary before either could observe
	// it, so these two need the sandbox to be reworked around them rather
	// than switched on.
	"internal/shared/tasks/taskstest": true,
	"internal/testsupport":            true,
}
