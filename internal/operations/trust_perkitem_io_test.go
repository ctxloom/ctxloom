package operations

import (
	"os"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingFs tallies reads per path so a claim about how much file I/O a code
// path does can be MEASURED rather than asserted in prose. It wraps a real
// OsFs: the subject is a real YAML parse of a real lock.yaml, and a MemMapFs
// would let a wrong implementation pass (template §6's asymmetry).
type countingFs struct {
	afero.Fs
	mu    sync.Mutex
	reads map[string]int
}

func newCountingFs() *countingFs {
	return &countingFs{Fs: afero.NewOsFs(), reads: map[string]int{}}
}

func (c *countingFs) tally(name string) {
	c.mu.Lock()
	c.reads[name]++
	c.mu.Unlock()
}

func (c *countingFs) readsOf(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads[name]
}

func (c *countingFs) Open(name string) (afero.File, error) {
	c.tally(name)
	return c.Fs.Open(name)
}

func (c *countingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	c.tally(name)
	return c.Fs.OpenFile(name, flag, perm)
}

// TestTrustStamper_ReadsTheLockfileOncePerItem measures a documentation gap.
// Both the stamper's resolve and contentGate's own doc used to claim the shared
// review-records store means "no per-item file I/O". Only half of that is true:
// the records store IS built once, but EffectiveTrust builds its RETRACTION
// records per call whenever the request carries none, and no production caller
// supplies one — contentGate.retraction is a test-only seam its builder never
// sets, and TrustStamper.resolve does not populate the field at all. So every
// stamped item opens and YAML-parses lock.yaml again.
//
// This pins the corrected prose. If the retraction records are ever hoisted to
// construction time (a real improvement, but one that changes WHEN retraction
// state is sampled — a trust-behaviour decision, not a sweep's), this test goes
// red and the comments must be revisited with it.
func TestTrustStamper_ReadsTheLockfileOncePerItem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	baseDir := t.TempDir()
	lockPath := writeLockYAML(t, baseDir, "version: 1\nbundles:\n  https://github.com/acme/repo@bundles/tooling:\n    sha: abc123\n")
	cfg := testConfigWithSCMPath(baseDir)

	fs := newCountingFs()
	fx := newTrustFixture(t)
	stamper := NewTrustStamper(cfg, WithStampLoader(stampSeed(t)), WithStampRecords(fx.records()), WithStampFS(fs))

	const acme = "https://github.com/acme/repo@bundles/"
	before := fs.readsOf(lockPath)
	stamper.ForRef(acme + "tooling#fragments/solid")
	stamper.ForRef(acme + "plain#fragments/pf")
	stamper.ForRef(acme + "banned#fragments/bad")

	require.Equal(t, 3, fs.readsOf(lockPath)-before,
		"the retraction record is re-read from lock.yaml for every stamped item — the shared records store covers approvals only")
}

// TestContentGate_ReadsTheLockfilePerItem is the same measurement through the
// exposure gate, whose own doc carried the identical claim. It matters more
// here: the gate is on the assembly hot path, where a whole bundle's items are
// resolved in one pass.
func TestContentGate_ReadsTheLockfilePerItem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	baseDir := t.TempDir()
	lockPath := writeLockYAML(t, baseDir, "version: 1\nbundles:\n  https://github.com/acme/repo@bundles/tooling:\n    sha: abc123\n")
	cfg := testConfigWithSCMPath(baseDir)

	fs := newCountingFs()
	// retraction left nil — exactly what buildContentGate produces in production.
	g := &contentGate{cfg: cfg, records: newTrustFixture(t).records(), fs: fs}

	before := fs.readsOf(lockPath)
	admitExec(t, g, execRead(t, ""), solidRef, pbytes("a"), rawForm)
	admitExec(t, g, execRead(t, ""), solidRef, pbytes("b"), rawForm)

	assert.Equal(t, 2, fs.readsOf(lockPath)-before,
		"the gate re-reads lock.yaml per gated item; only its review-records store is built once")
}
