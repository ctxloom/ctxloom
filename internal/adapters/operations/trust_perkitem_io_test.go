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

// The retraction record is read ONCE per gate, when the gate is built, and
// every item decides with that read. That is the generation model: a Trust
// belongs to one config generation, and the lockfile it read cannot outlive
// it — a pull that rewrites the lockfile produces the next generation. These
// tests MEASURE it against a real lock.yaml so the claim cannot drift back to
// per-item sampling unnoticed.
func TestTrustStamper_ReadsTheLockfileOncePerGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	baseDir := t.TempDir()
	lockPath := writeLockYAML(t, baseDir, "version: 1\nbundles:\n  https://github.com/acme/repo@bundles/tooling:\n    sha: abc123\n")
	cfg := testConfigWithSCMPath(baseDir)

	fs := newCountingFs()
	fx := newTrustFixture(t)
	before := fs.readsOf(lockPath)
	stamper := NewTrustStamper(cfg, WithStampLoader(stampSeed(t)), WithStampRecords(fx.records()), WithStampFS(fs))

	const acme = "https://github.com/acme/repo@bundles/"
	stamper.ForRef(acme + "tooling#fragments/solid")
	stamper.ForRef(acme + "plain#fragments/pf")
	stamper.ForRef(acme + "banned#fragments/bad")

	require.Equal(t, 1, fs.readsOf(lockPath)-before,
		"the retraction record is read ONCE when the gate is built (remote.NewLockfileRetraction) and never per item: a pull that rewrites the lockfile produces the next generation's records, not this one's")
}

func TestContentGate_ReadsTheLockfileOncePerGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	baseDir := t.TempDir()
	lockPath := writeLockYAML(t, baseDir, "version: 1\nbundles:\n  https://github.com/acme/repo@bundles/tooling:\n    sha: abc123\n")
	cfg := testConfigWithSCMPath(baseDir)

	fs := newCountingFs()
	g := &contentGate{cfg: cfg, records: newTrustFixture(t).records(), fs: fs}

	before := fs.readsOf(lockPath)
	admitExec(t, g, execRead(t, ""), solidDecideRef, pbytes("a"), rawForm)
	admitExec(t, g, execRead(t, ""), solidDecideRef, pbytes("b"), rawForm)

	assert.Equal(t, 1, fs.readsOf(lockPath)-before,
		"the gate reads lock.yaml once, when it is built; every gated item decides with that one read")
}
