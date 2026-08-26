package cli

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// One noun, one question. `remote` answers "where does content come from" —
// the REGISTERED SOURCE, pure local bookkeeping over remotes.yaml. `deps`
// answers "what do I have installed" — the LOCAL CLOSURE, the lockfile and
// everything that moves it.
//
// The two were one noun, and the cost was paid at the prompt: `remote pull`
// reads as "pull the remote", which is not what it does — it installs THIS
// project's dependency closure, and the remote it names is not even an
// argument. Someone looking for what they have installed had no noun to type.
//
// These tests assert the surface as a SET rather than leaf by leaf, because a
// move can half-finish: adding `deps pull` while `remote pull` still answers
// leaves two spellings for one behaviour, and a per-leaf existence check is
// blind to the one that should be gone.

// leafNames returns every subcommand name and alias directly under path.
func leafNames(t *testing.T, path ...string) []string {
	t.Helper()
	cmd := rootCommand()
	for _, name := range path {
		sub := findSub(cmd, name)
		require.NotNil(t, sub, "`ctxloom %s` exists", strings.Join(path, " "))
		cmd = sub
	}
	var names []string
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
		names = append(names, c.Aliases...)
	}
	sort.Strings(names)
	return names
}

// TestDeps_CarriesTheLocalClosure pins what the noun holds.
func TestDeps_CarriesTheLocalClosure(t *testing.T) {
	got := leafNames(t, "deps")

	for _, leaf := range []string{"pull", "upgrade", "check", "hold", "unhold", "list"} {
		assert.Contains(t, got, leaf, "`ctxloom deps %s` is a leaf of the closure noun", leaf)
	}
}

// TestRemote_CarriesOnlyTheRegisteredSource is the other half: what `remote`
// no longer answers. Every name here moved to `deps`, and leaving a second
// spelling behind is the failure this asserts against.
func TestRemote_CarriesOnlyTheRegisteredSource(t *testing.T) {
	got := leafNames(t, "remote")

	for _, leaf := range []string{"create", "remove", "list", "show", "default", "discover"} {
		assert.Contains(t, got, leaf, "`ctxloom remote %s` is registry or discovery", leaf)
	}
	for _, moved := range []string{"pull", "upgrade", "update", "check", "hold", "unhold"} {
		assert.NotContains(t, got, moved,
			"`ctxloom remote %s` moved to `deps`; two spellings for one behaviour is the half-finished move", moved)
	}
}

// TestBundle_NoLongerHoldsAPin. A hold freezes a LOCKFILE entry, which is the
// closure's state, not the bundle's — `bundle hold` put a dependency-management
// verb on the content noun, where nothing else it sits beside touches the lock.
func TestBundle_NoLongerHoldsAPin(t *testing.T) {
	got := leafNames(t, "bundle")

	assert.NotContains(t, got, "hold", "holding a pin is `ctxloom deps hold`")
	assert.NotContains(t, got, "unhold", "releasing a hold is `ctxloom deps unhold`")
}

// TestDepsHold_CarriesNoPinAlias. "pin" is already taken: a manifest pin is an
// exact version constraint the author WROTE, and a hold is a local policy that
// overrides what the constraint allows. One word for both makes the two
// indistinguishable in every message that names it.
func TestDepsHold_CarriesNoPinAlias(t *testing.T) {
	got := leafNames(t, "deps")

	assert.NotContains(t, got, "pin", "a hold is not a manifest pin")
	assert.NotContains(t, got, "unpin", "a hold is not a manifest pin")
}

// TestDepsCheck_TakesNoApplyFlag. `check` reads and `upgrade` writes; a flag
// that turns the read into the write puts two behaviours behind one word and
// leaves the safe spelling one keystroke from the destructive one.
func TestDepsCheck_TakesNoApplyFlag(t *testing.T) {
	deps := findSub(rootCommand(), "deps")
	require.NotNil(t, deps)
	check := findSub(deps, "check")
	require.NotNil(t, check)

	for _, flag := range []string{"apply", "cleanup"} {
		assert.Nil(t, check.Flags().Lookup(flag),
			"`deps check` reads; --%s belongs to the verb that writes", flag)
	}
}

// TestDepsBare_ListsTheClosure. `deps` on its own answers the question
// somebody typing it almost certainly has — what do I have installed — through
// the same groupNodeDefault seam `remote` uses.
func TestDepsBare_ListsTheClosure(t *testing.T) {
	remoteBareFixture(t)
	presentTerminal(t, true)

	bare, err := runRoot(t, "deps")
	require.NoError(t, err)
	listed, err := runRoot(t, "deps", "list")
	require.NoError(t, err)

	assert.Equal(t, listed, bare, "bare `ctxloom deps` is the same entry point as `ctxloom deps list`")
	assert.NotContains(t, bare, usageMarker, "bare `ctxloom deps` answers; help has its own spelling")
}

// TestDepsList_IsOfflineAndSaysSoWhenEmpty. The listing reads the lockfile and
// nothing else — it is the one thing you can ask with no network, no
// credential and a remote that has gone away. A project with nothing installed
// must say that, and say what to run: an empty listing and a broken listing
// are the same pixels.
func TestDepsList_IsOfflineAndSaysSoWhenEmpty(t *testing.T) {
	remoteBareFixture(t)

	// `--format text` is the subject here, not a way around the
	// machine-readable default a non-terminal resolves to: an empty JSON array
	// is a complete and correct answer that says nothing about what to run
	// next. The remedy sentence exists only in the human rendering, so the
	// human rendering is what this asks for.
	out, err := runRoot(t, "deps", "list", "--format", "text")

	require.NoError(t, err)
	assert.Contains(t, out, "No dependencies installed")
	assert.Contains(t, out, "ctxloom deps pull")
}

// TestDepsList_RendersPinHoldAndOrigin drives the renderer from a value, so
// the four facts a closure listing exists to carry are asserted without a
// lockfile fixture or a config load: which bundle, what commit it sits on,
// whether a hold freezes it, and which registered remote it came from.
func TestDepsList_RendersPinHoldAndOrigin(t *testing.T) {
	var b strings.Builder

	require.NoError(t, renderDepsList(&b, &depsListing{Deps: []installedDep{
		{Name: "demo", Ref: "https://github.com/alice/ctxloom@bundles/demo", SHA: "0123456789abcdef", Origin: "alice"},
		{Name: "security", Ref: "https://github.com/corp/ctxloom@bundles/security", SHA: "fedcba9876543210", Held: true, Origin: "corp"},
	}}))
	out := b.String()

	assert.Contains(t, out, "demo")
	assert.Contains(t, out, "0123456", "the listing names the commit each dependency sits on")
	assert.Contains(t, out, "alice", "the listing names the remote each dependency came from")
	assert.Contains(t, out, "held", "a frozen dependency says so; a hold nobody can see is a hold nobody trusts")
	assert.NotContains(t, strings.SplitN(out, "security", 2)[0], "held",
		"only the held dependency is marked held")
}

// TestDepsList_NamesAnUnregisteredOrigin. A lockfile outlives the registry —
// somebody removes a remote, or clones a project whose remotes.yaml they never
// got. The row must still say where the content came from rather than printing
// a blank column, which reads as "local".
func TestDepsList_NamesAnUnregisteredOrigin(t *testing.T) {
	var b strings.Builder

	require.NoError(t, renderDepsList(&b, &depsListing{Deps: []installedDep{
		{Name: "orphan", Ref: "https://git.example.com/team/ctxloom@bundles/orphan", SHA: "abc1234", Origin: ""},
	}}))

	assert.Contains(t, b.String(), "git.example.com",
		"with no registered remote to name, the row falls back to the URL it was pulled from")
}

// TestDepsList_ReadsAnInstalledClosureFromTheLockfile drives the whole read
// path — lockfile load, reference parse, origin lookup — against a real
// lockfile on disk.
//
// The renderer tests above are driven from a value, which proves the four facts
// are PRINTABLE but not that any of them is ever READ. This is what fails if
// the load stops carrying the hold flag, loses the constraint, or derives a
// name nobody could pass back to `deps hold`.
func TestDepsList_ReadsAnInstalledClosureFromTheLockfile(t *testing.T) {
	root, cfg := setupProject(t, "mock")
	t.Chdir(root)

	const (
		demoRef  = "https://github.com/alice/ctxloom@bundles/demo"
		guardRef = "https://github.com/alice/ctxloom@bundles/guardrails"
	)
	manager := remote.NewLockfileManager(projectAppDir(cfg))
	lockfile, err := manager.Load()
	require.NoError(t, err)
	lockfile.AddEntry(remote.ItemTypeBundle, demoRef, remote.LockEntry{
		SHA: "0123456789abcdef0123456789abcdef01234567",
		URL: "https://github.com/alice/ctxloom", RequestedVersion: "^1.2",
	})
	lockfile.AddEntry(remote.ItemTypeBundle, guardRef, remote.LockEntry{
		SHA: "fedcba9876543210fedcba9876543210fedcba98",
		URL: "https://github.com/alice/ctxloom", Held: true,
	})
	require.NoError(t, manager.Save(lockfile))

	listing, err := loadDepsListing(context.Background(), cfg)
	require.NoError(t, err)

	require.Len(t, listing.Deps, 2)
	assert.Equal(t, 2, listing.Count)
	// Sorted by name, so two runs over one lockfile print the same thing —
	// AllEntries walks a map, whose order Go deliberately randomizes.
	assert.Equal(t, "demo", listing.Deps[0].Name, "the name is the one `deps hold` takes")
	assert.Equal(t, "guardrails", listing.Deps[1].Name)

	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", listing.Deps[0].SHA)
	assert.Equal(t, "^1.2", listing.Deps[0].Constraint)
	assert.False(t, listing.Deps[0].Held)
	assert.True(t, listing.Deps[1].Held, "the hold flag survives the read; an invisible hold is an untrusted one")
}
