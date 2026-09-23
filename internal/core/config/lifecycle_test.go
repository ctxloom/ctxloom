package config_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/ctxloom/ctxloom/internal/testsupport"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// fakeSources is a config.Sources whose every port is a closure, with a count
// of Read calls: the lifecycle's contract is stated in how many times and in
// which order the owner consults its sources, so the fake records exactly
// that and nothing else.
type fakeSources struct {
	reads   atomic.Int32
	read    func(context.Context) (*config.Config, []config.Warning, error)
	readers func(*config.Config) []bundles.Reader
	ports   func(*config.Config) []compositetest.Option
}

func (f *fakeSources) Read(ctx context.Context) (*config.Config, []config.Warning, error) {
	f.reads.Add(1)
	return f.read(ctx)
}

func (f *fakeSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	if f.readers == nil {
		return nil, nil
	}
	return f.readers(cfg), nil
}

func (f *fakeSources) TrustPorts(_ context.Context, cfg *config.Config) (composite.TrustRoot, composite.ReviewRecords, composite.RetractionRecords, error) {
	var opts []compositetest.Option
	if f.ports != nil {
		opts = f.ports(cfg)
	}
	root, records, retraction := compositetest.Ports(opts...)
	return root, records, retraction, nil
}

// sequenceSources returns a Sources whose Read hands back the given configs
// in order, each one a distinct value so a generation can be told apart from
// its predecessor by identity as well as by content.
func sequenceSources(cfgs ...*config.Config) *fakeSources {
	var i atomic.Int32
	return &fakeSources{read: func(context.Context) (*config.Config, []config.Warning, error) {
		n := int(i.Add(1)) - 1
		if n >= len(cfgs) {
			n = len(cfgs) - 1
		}
		return cfgs[n], nil, nil
	}}
}

func fixtureWithDefault(agent string) *config.Config {
	return config.NewFixture(config.Fixture{DefaultAgent: agent})
}

func TestOpen_ReadsOnceAndPublishesGenerationOne(t *testing.T) {
	src := sequenceSources(fixtureWithDefault("first"))

	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)

	snap := owner.Current()
	require.NotNil(t, snap, "Open publishes the first generation; Current() must not be nil before any Reload")
	assert.Equal(t, uint64(1), snap.Generation)
	assert.Equal(t, "first", snap.Config.GetDefaultAgent())
	assert.False(t, snap.LoadedAt.IsZero())
	assert.Equal(t, int32(1), src.reads.Load(), "Open reads its sources exactly once")
}

func TestOpen_ReadError_RefusesWithoutOwner(t *testing.T) {
	boom := errors.New("config.yaml: yaml: line 3: mapping values are not allowed")
	src := &fakeSources{read: func(context.Context) (*config.Config, []config.Warning, error) {
		return nil, nil, boom
	}}

	owner, err := config.Open(context.Background(), src)
	require.ErrorIs(t, err, boom)
	assert.Nil(t, owner, "a process whose configuration cannot be read has no owner to hand out")
}

func TestOwner_Reload_NewGenerationLeavesOldSnapshotUnchanged(t *testing.T) {
	src := sequenceSources(fixtureWithDefault("first"), fixtureWithDefault("second"))
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	before := owner.Current()

	after, err := owner.Reload(context.Background())
	require.NoError(t, err)

	assert.Same(t, after, owner.Current(), "Reload publishes the snapshot it returns")
	assert.NotSame(t, before, after, "a Reload is a NEW generation, never a mutation of the published one")
	assert.Equal(t, uint64(2), after.Generation)
	assert.Equal(t, "second", after.Config.GetDefaultAgent())

	assert.Equal(t, uint64(1), before.Generation, "the retired snapshot keeps its generation")
	assert.Equal(t, "first", before.Config.GetDefaultAgent(), "the retired snapshot keeps its config value")
	assert.Equal(t, int32(2), src.reads.Load(), "one Read per generation")
}

func TestOwner_Reload_TrustIsBuiltPerGenerationFromTrustPorts(t *testing.T) {
	// Generation 1's records approve nothing; generation 2's approve
	// everything. The snapshot's Trust must decide with the ports of ITS
	// generation, so a review that lands between two reloads is visible on
	// the next one and never retroactively on the previous.
	var gen atomic.Int32
	src := sequenceSources(fixtureWithDefault("a"), fixtureWithDefault("b"))
	src.ports = func(*config.Config) []compositetest.Option {
		if gen.Add(1) == 1 {
			return nil
		}
		return []compositetest.Option{compositetest.ApproveAll()}
	}
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	first := owner.Current()
	e := remoteExposure(t)
	assert.False(t, first.Trust.Authorizer().Admit(e).Allow, "generation 1's records approve nothing")

	second, err := owner.Reload(context.Background())
	require.NoError(t, err)
	assert.True(t, second.Trust.Authorizer().Admit(e).Allow, "generation 2's records approve")
	assert.False(t, first.Trust.Authorizer().Admit(e).Allow, "the retired generation's trust is unchanged")
	assert.True(t, second.Trust.Gates(), "a generation's Trust always decides; only a listing names Ungated()")
}

// remoteExposure is an unsigned command that travelled: admitted by nothing
// but a review record.
func remoteExposure(t *testing.T) bundles.Exposure {
	t.Helper()
	const refStr = "ctxloom+git://github.com/acme/repo//bundles/tools#prompts/deploy"
	br, err := trust.ParseBundleRef(refStr)
	require.NoError(t, err)
	read := bundles.NewRead("tools", &bundles.Bundle{Name: "tools"}, bundles.ProvenanceRemote, bundles.TrustCtxRemote,
		bundles.SignatureFacts{Signature: bundles.SignatureNone, Signer: bundles.SignerNone})
	return bundles.Exposure{Read: read, Ref: trust.RefFromBundleRef(br), RefStr: refStr, Bytes: []byte("echo"), Form: bundles.FormRaw}
}

func TestOwner_Reload_CatalogIsResolvedFromReaders(t *testing.T) {
	memfs := afero.NewMemMapFs()
	bundletree.Write(t, memfs, paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), "only", "version: \"1.0\"\n")

	src := sequenceSources(fixtureWithDefault("a"))
	src.readers = func(*config.Config) []bundles.Reader {
		return []bundles.Reader{bundles.NewProjectReader(memfs, []string{"/bundles"})}
	}
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)

	reads := owner.Current().Catalog().Reads()
	require.Len(t, reads, 1, "the snapshot's catalog is resolved from Sources.Readers")
	assert.Equal(t, "only", reads[0].Bundle.Name)
}

func TestOwner_Update_WritesThroughAndReturnsNextGeneration(t *testing.T) {
	memfs := afero.NewMemMapFs()
	const appDir = "/proj/.ctxloom"
	require.NoError(t, memfs.MkdirAll(appDir, 0o755))
	testsupport.WriteFile(t, memfs, appDir+"/config.yaml", []byte("default_agent: first\n"), 0o644)

	// The fake's Read is a real parse of the file the fake owns, so a write
	// that reached disk is observable as a changed value on the next Read.
	src := &fakeSources{read: func(context.Context) (*config.Config, []config.Warning, error) {
		data, err := afero.ReadFile(memfs, appDir+"/config.yaml")
		if err != nil {
			return nil, nil, err
		}
		parsed, err := config.ParseConfig(data)
		if err != nil {
			return nil, nil, err
		}
		f := parsed.ToFixture()
		f.AppDir, f.AppPaths, f.AppRoot, f.Source = appDir, []string{appDir}, "/proj", config.SourceProject
		cfg := config.NewFixture(f)
		cfg.SetFS(memfs)
		return cfg, nil, nil
	}}
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	before := owner.Current()

	after, err := owner.Update(context.Background(), func(d *config.Draft) error {
		d.DefaultAgent = "second"
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, before.Generation+1, after.Generation, "Update produces generation N+1")
	assert.Same(t, after, owner.Current())
	assert.Equal(t, "second", after.Config.GetDefaultAgent())
	assert.Equal(t, "first", before.Config.GetDefaultAgent(), "the previous generation is untouched by the write")

	data, err := afero.ReadFile(memfs, appDir+"/config.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(data), "default_agent: second", "Update writes through to the file")
}

func TestOwner_Update_FnError_AbandonsWriteAndKeepsGeneration(t *testing.T) {
	memfs := afero.NewMemMapFs()
	const appDir = "/proj/.ctxloom"
	testsupport.WriteFile(t, memfs, appDir+"/config.yaml", []byte("default_agent: first\n"), 0o644)
	cfg := config.NewFixture(config.Fixture{DefaultAgent: "first", AppDir: appDir, AppPaths: []string{appDir}})
	cfg.SetFS(memfs)
	src := sequenceSources(cfg)
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	before := owner.Current()

	abandon := errors.New("abandon")
	_, err = owner.Update(context.Background(), func(*config.Draft) error { return abandon })
	require.ErrorIs(t, err, abandon)
	assert.Same(t, before, owner.Current(), "an abandoned Update publishes nothing")
	data, err := afero.ReadFile(memfs, appDir+"/config.yaml")
	require.NoError(t, err)
	assert.Equal(t, "default_agent: first\n", string(data), "an abandoned Update writes nothing")
}

// A generation captures its readers at Reload but resolves them on first
// use: a consumer that never reads bundles never executes a reader — the
// companion probe execs binaries, and merely reading a config value must
// not run one.
func TestOwner_Reload_CatalogResolvesOnFirstUseOnly(t *testing.T) {
	var reads atomic.Int32
	src := sequenceSources(fixtureWithDefault("a"))
	src.readers = func(*config.Config) []bundles.Reader {
		return []bundles.Reader{countingReader{reads: &reads}}
	}
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, int32(0), reads.Load(), "Open captures the readers without resolving them")

	snap := owner.Current()
	_ = snap.Catalog()
	_ = snap.Catalog()
	_ = snap.Config.Catalog()
	assert.Equal(t, int32(1), reads.Load(), "the generation resolves its readers exactly once, however many consumers ask")
}

type countingReader struct{ reads *atomic.Int32 }

func (r countingReader) Read(context.Context) ([]bundles.BundleRead, error) {
	r.reads.Add(1)
	return nil, nil
}
