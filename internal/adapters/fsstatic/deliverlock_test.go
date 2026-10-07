package fsstatic_test

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/delivery/deliverytest"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// deadlockBound is how long a test waits for two takers it has released to
// finish before calling it a deadlock. It synchronizes nothing: every
// interleaving below is forced through the lock tracker and the commit seam,
// and this only turns a hang into a failure that names itself.
const deadlockBound = 10 * time.Second

// lockTracker sees every lock each named taker takes through the Roots it
// hands out, so a test can learn — without sleeping — that one taker is about
// to wait on a lock another holds.
type lockTracker struct {
	mu      sync.Mutex
	holder  map[string]string
	blocked map[string]chan string
}

func newLockTracker() *lockTracker {
	return &lockTracker{holder: map[string]string{}, blocked: map[string]chan string{}}
}

// root is base with its locks seen as who's.
func (tr *lockTracker) root(base safefs.Root, who string) safefs.Root {
	base.Locks = trackedLocks{Locks: base.Locks, tr: tr, who: who}
	return base
}

// blockedOn receives the lock path who is about to wait on while another
// taker holds it.
func (tr *lockTracker) blockedOn(who string) chan string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	ch, ok := tr.blocked[who]
	if !ok {
		ch = make(chan string, 1)
		tr.blocked[who] = ch
	}
	return ch
}

func (tr *lockTracker) waiting(who, path string) {
	ch := tr.blockedOn(who)
	tr.mu.Lock()
	h, held := tr.holder[filepath.Clean(path)]
	tr.mu.Unlock()
	if held && h != who {
		select {
		case ch <- path:
		default:
		}
	}
}

func (tr *lockTracker) set(who, path string, held bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if held {
		tr.holder[filepath.Clean(path)] = who
	} else {
		delete(tr.holder, filepath.Clean(path))
	}
}

type trackedLocks struct {
	safefs.Locks
	tr  *lockTracker
	who string
}

func (l trackedLocks) Lock(path string) (safefs.Lock, error) {
	return l.take(path, l.Locks.Lock)
}

func (l trackedLocks) RLock(path string) (safefs.Lock, error) {
	return l.take(path, l.Locks.RLock)
}

func (l trackedLocks) take(path string, take func(string) (safefs.Lock, error)) (safefs.Lock, error) {
	l.tr.waiting(l.who, path)
	lk, err := take(path)
	if err != nil {
		return nil, err
	}
	l.tr.set(l.who, path, true)
	return trackedLock{Lock: lk, tr: l.tr, path: path}, nil
}

type trackedLock struct {
	safefs.Lock
	tr   *lockTracker
	path string
}

func (l trackedLock) Unlock() error {
	l.tr.set("", l.path, false)
	return l.Lock.Unlock()
}

// commandLoadout is a loadout delivering one mock command named name into
// project's commands dir.
func commandLoadout(t *testing.T, project, name string) delivery.Loadout {
	t.Helper()
	eng := mock.New()
	pkg := compositetest.Fixture(t, compositetest.WithCommand(name, "body of "+name))
	items := pkg.EngineItems(eng.Root().Name)
	exports, err := eng.Exports(items)
	require.NoError(t, err)
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{present.Commands: present.RootProjectRoot}}
	plan, err := delivery.Route(items, eng.Root(), pref, present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Static, "the plan must route the command")
	return delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}
}

// requireLedgerIsDisk holds dir's commands ledger to exactly the command
// files standing in dir, and returns them: an entry with no file, or a file
// no entry claims, is what an interleaved delivery leaves behind.
func requireLedgerIsDisk(t *testing.T, files safefs.Root, dir string) []string {
	t.Helper()
	recorded, err := ledger.Ledger{Root: files, Dir: dir}.Read(ledger.SurfaceCommands)
	require.NoError(t, err)
	var onDisk []string
	entries, err := afero.ReadDir(files.Fs, dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Name() != ledger.Name {
			onDisk = append(onDisk, e.Name())
		}
	}
	sort.Strings(recorded)
	require.Equal(t, onDisk, recorded, "the ledger must record exactly the files in %s", dir)
	return recorded
}

// writeCommand writes one command, name, into dir through the shared
// managed-file writer: under dir's lock, recorded in dir's ledger.
func writeCommand(files safefs.Root, dir, name string) error {
	return agent.WriteManagedCommandFiles(files, dir,
		[]agent.CommandExport{{Name: name, Content: "body of " + name, Enabled: true}},
		func(c agent.CommandExport) (string, []byte, error) { return c.Name + ".md", []byte(c.Content), nil })
}

// managedCommands is the mock engine with its commands approach writing each
// command into dir through the shared managed-file writer.
func managedCommands(dir string) engine.Base {
	root := mock.New().Root()
	root.Commands = commandsBy{CommandsApproach: root.Commands, deliver: func(files safefs.Root) (present.Delivered, error) {
		return present.Delivered{}, writeCommand(files, dir, "b")
	}}
	return root
}

// awaitParked waits for a taker to reach its park, failing if it finishes
// first or never gets there.
func awaitParked(t *testing.T, who string, parked <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-parked:
	case err := <-done:
		t.Fatalf("%s finished before reaching its park: %v", who, err)
	case <-time.After(deadlockBound):
		t.Fatalf("deadlock: %s did not reach its park within %s", who, deadlockBound)
	}
}

// awaitBlockedOrDone waits until who is about to wait on a lock another
// taker holds, or has finished; a finish is put back for awaitBoth.
func awaitBlockedOrDone(t *testing.T, tr *lockTracker, who string, done chan error) {
	t.Helper()
	select {
	case <-tr.blockedOn(who):
	case err := <-done:
		done <- err
	case <-time.After(deadlockBound):
		t.Fatalf("deadlock: %s neither finished nor waited on another's lock within %s", who, deadlockBound)
	}
}

// awaitBoth waits for both takers' results within deadlockBound.
func awaitBoth(t *testing.T, a, b <-chan error) (aErr, bErr error) {
	t.Helper()
	deadline := time.After(deadlockBound)
	for got := 0; got < 2; got++ {
		select {
		case aErr = <-a:
			a = nil
		case bErr = <-b:
			b = nil
		case <-deadline:
			t.Fatalf("deadlock: two takers released to run did not both finish within %s", deadlockBound)
		}
	}
	return aErr, bErr
}

// TestDeliver_TwoDeliveriesIntoOneDirectoryDoNotInterleave forces the
// interleaving a delivery's locks exist to exclude: delivery B runs its
// approach (having read its writer's previous files from the record), and is
// parked before its commit; delivery A into the SAME dir is
// then started. A must not complete inside B's window — had it, B would
// commit a ledger built from what it read before A's files landed, and A's
// file would stand beside B's: B's release of the writer's previous files was
// read before A's landed. Whichever order the two land in, what stands on
// disk is one delivery's set, and the record names exactly it.
func TestDeliver_TwoDeliveriesIntoOneDirectoryDoNotInterleave(t *testing.T) {
	fs := afero.NewOsFs()
	base := safefs.NewMem(fs)
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	project := t.TempDir()
	root := mock.New().Root()
	target := delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriter}
	loA, loB := commandLoadout(t, project, "a"), commandLoadout(t, project, "b")

	tr := newLockTracker()
	stA := fsstatic.New(tr.root(base, "A"))
	stB := fsstatic.New(tr.root(base, "B"))
	parked, resume := make(chan struct{}), make(chan struct{})
	fsstatic.SetBeforeCommit(stB, func() { close(parked); <-resume })

	bDone, aDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := stB.Deliver(context.Background(), loB, root, target); bDone <- err }()
	awaitParked(t, "B", parked, bDone)
	go func() { _, err := stA.Deliver(context.Background(), loA, root, target); aDone <- err }()
	awaitBlockedOrDone(t, tr, "A", aDone)
	close(resume)
	aErr, bErr := awaitBoth(t, aDone, bDone)
	require.NoError(t, bErr)
	require.NoError(t, aErr)

	delivered := deliverytest.RelativeFiles(fs, project)
	require.Len(t, delivered, 1, "one delivery's set replaces the other's, never their union: %v", delivered)
	owned, err := rec.Targets(delivery.ProjectWriter)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(project, delivered[0])}, owned, "the record names exactly the file on disk")
}

// TestDeliver_HoldsTheDirectoryLockThroughItsCommit: a writer of a managed
// dir that is not this static writer — the shared managed-file writer
// called on the real filesystem — takes the dir's lock and does its whole
// cycle under it. A delivery parked between its approach run and its commit
// still holds that dir's lock, so the direct writer waits until the
// delivery's files are on disk and reads them, rather than completing in the
// delivery's window and having its ledger overwritten by the delivery's
// stale one.
func TestDeliver_HoldsTheDirectoryLockThroughItsCommit(t *testing.T) {
	fs := afero.NewOsFs()
	base := safefs.NewMem(fs)
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	project := t.TempDir()
	dir := filepath.Join(project, mock.ConfigDirName, "commands")
	root := managedCommands(dir)
	target := delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriter}
	lo := commandLoadout(t, project, "b")

	tr := newLockTracker()
	st := fsstatic.New(tr.root(base, "B"))
	parked, resume := make(chan struct{}), make(chan struct{})
	fsstatic.SetBeforeCommit(st, func() { close(parked); <-resume })

	bDone, wDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := st.Deliver(context.Background(), lo, root, target); bDone <- err }()
	awaitParked(t, "B", parked, bDone)
	go func() {
		wDone <- writeCommand(tr.root(base, "W"), dir, "w")
	}()
	awaitBlockedOrDone(t, tr, "W", wDone)
	close(resume)
	wErr, bErr := awaitBoth(t, wDone, bDone)
	require.NoError(t, bErr)
	require.NoError(t, wErr)
	require.Equal(t, []string{"w.md"}, requireLedgerIsDisk(t, base, dir), "the direct writer ran last, over the delivery's files")
}

// TestDeliver_DeliveriesTakingOverlappingDirsInOppositeOrdersDoNotDeadlock:
// a delivery holds every managed dir it wrote until its commit, so two
// deliveries that take two dirs in opposite orders would each hold one and
// wait on the other. The interleaving is forced: A writes its first dir and
// is parked; B is then started and must either finish or wait on a lock A
// holds before A resumes. Both must then finish.
func TestDeliver_DeliveriesTakingOverlappingDirsInOppositeOrdersDoNotDeadlock(t *testing.T) {
	fs := afero.NewOsFs()
	base := safefs.NewMem(fs)
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	project := t.TempDir()
	one, two := filepath.Join(project, "one"), filepath.Join(project, "two")
	target := delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriter}

	// inOrder is a commands approach writing a command into each of dirs in
	// turn, calling between after the first.
	inOrder := func(name string, between func(), dirs ...string) engine.Base {
		root := mock.New().Root()
		root.Commands = commandsBy{CommandsApproach: root.Commands, deliver: func(files safefs.Root) (present.Delivered, error) {
			for i, dir := range dirs {
				if err := writeCommand(files, dir, name); err != nil {
					return present.Delivered{}, err
				}
				if i == 0 {
					between()
				}
			}
			return present.Delivered{}, nil
		}}
		return root
	}
	tr := newLockTracker()
	parked, resume := make(chan struct{}), make(chan struct{})
	rootA := inOrder("a", func() { close(parked); <-resume }, one, two)
	rootB := inOrder("b", func() {}, two, one)
	loA, loB := commandLoadout(t, project, "a"), commandLoadout(t, project, "b")

	aDone, bDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := fsstatic.New(tr.root(base, "A")).Deliver(context.Background(), loA, rootA, target)
		aDone <- err
	}()
	awaitParked(t, "A", parked, aDone)
	go func() {
		_, err := fsstatic.New(tr.root(base, "B")).Deliver(context.Background(), loB, rootB, target)
		bDone <- err
	}()
	awaitBlockedOrDone(t, tr, "B", bDone)
	close(resume)
	aErr, bErr := awaitBoth(t, aDone, bDone)
	require.NoError(t, aErr)
	require.NoError(t, bErr)
	requireLedgerIsDisk(t, base, one)
	requireLedgerIsDisk(t, base, two)
}
