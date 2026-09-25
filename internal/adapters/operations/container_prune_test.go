package operations

import (
	"context"
	"errors"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestContainerPrune_NoRuntimeIsANonDegradableIsolationFinding: asked to prune
// with no runtime present, the service records the ClassIsolation finding the
// CLI's gate turns into exit 3 — under --degraded too — and plans nothing.
func TestContainerPrune_NoRuntimeIsANonDegradableIsolationFinding(t *testing.T) {
	app, _ := doctorApp(t)
	orig := pruneAvailableRuntimes
	pruneAvailableRuntimes = func() []isolation.Runtime { return nil }
	t.Cleanup(func() { pruneAvailableRuntimes = orig })

	mark := strictness.Checkpoint()
	rep, err := ContainerPrune(context.Background(), app, ContainerPruneRequest{})
	require.NoError(t, err)
	assert.Empty(t, rep.Runtimes)
	found := strictness.Since(mark)
	require.Len(t, found, 1)
	assert.Equal(t, report.KindIsolation, found[0].Kind)
	assert.True(t, found[0].NonDegradable)
}

// TestContainerPrune_UnknownRuntimeIsAUsageError: a typo is not "no runtime".
func TestContainerPrune_UnknownRuntimeIsAUsageError(t *testing.T) {
	app, _ := doctorApp(t)
	_, err := ContainerPrune(context.Background(), app, ContainerPruneRequest{Runtime: "dokcer"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker|podman")
}

// TestContainerPrune_HostPlansNothing: a runtime with no binary yields an
// empty section and no failure — nothing is probed or removed.
func TestContainerPrune_HostPlansNothing(t *testing.T) {
	app, _ := doctorApp(t)
	orig := pruneAvailableRuntimes
	pruneAvailableRuntimes = func() []isolation.Runtime { return []isolation.Runtime{isolation.Host{}} }
	t.Cleanup(func() { pruneAvailableRuntimes = orig })
	rep, err := ContainerPrune(context.Background(), app, ContainerPruneRequest{Apply: true})
	require.NoError(t, err)
	require.Len(t, rep.Runtimes, 1)
	assert.Equal(t, "host", rep.Runtimes[0].Runtime)
	assert.False(t, rep.Failed())
	assert.Equal(t, DefaultImagePruneMinAge, rep.MinAge)
}

// TestImagePruneOptions_LiveIsTheConfiguredIdentity: the current-identity
// rule is fed the ref a containerized run of the project's backend resolves.
func TestImagePruneOptions_LiveIsTheConfiguredIdentity(t *testing.T) {
	app, _ := doctorApp(t)
	cfg, err := app.Config(context.Background())
	require.NoError(t, err)
	opts := imagePruneOptions(app.Engines(), cfg, isolation.Host{}, time.Hour, time.Unix(0, 0))
	want, ok := isolation.LiveImageRef(isolation.Host{}, "claude-code", isolation.ImageConfig{AppRoot: cfg.GetAppRoot()})
	require.True(t, ok)
	assert.Contains(t, opts.Live, want)
	assert.Equal(t, time.Hour, opts.MinAge)
	assert.Empty(t, imagePruneOptions(app.Engines(), nil, isolation.Host{}, time.Hour, time.Unix(0, 0)).Live,
		"no config, no live refs — and --apply refuses in that state")
}

func TestContainerPruneReport_Failed(t *testing.T) {
	assert.False(t, ContainerPruneReport{Runtimes: []ContainerPruneRuntime{{Images: []ContainerPruneImage{{Action: PruneRemove}}}}}.Failed())
	assert.True(t, ContainerPruneReport{Runtimes: []ContainerPruneRuntime{{Images: []ContainerPruneImage{{Action: PruneFailed}}}}}.Failed())
	assert.True(t, ContainerPruneReport{Runtimes: []ContainerPruneRuntime{{Error: "x"}}}.Failed())
}

func TestFormatImageBytes(t *testing.T) {
	assert.Equal(t, "999 B", FormatImageBytes(999))
	assert.Equal(t, "42.1 MB", FormatImageBytes(42_100_000))
	assert.Equal(t, "13.8 GB", FormatImageBytes(13_840_000_000))
}

// --- DOCTOR-CHECK-SUPERSEDED-IMAGES-x4 ------------------------------------

// TestDoctorCheckSupersededImages: the nudge plans every runtime present,
// says nothing is wrong when none holds a superseded image, and warns —
// naming the runtime and the reclaimable bytes — when one does.
func TestDoctorCheckSupersededImages(t *testing.T) {
	none := doctorCheckSupersededImages(context.Background(), nil, nil)
	assert.Equal(t, DoctorInfo, none.Status, none.Detail)

	superseded := isolation.ImagePrunePlan{Verdicts: []isolation.ImageVerdict{
		{Image: isolation.OwnedImage{ID: "a", Size: 2_000_000_000}},
		{Image: isolation.OwnedImage{ID: "b", Size: 500_000_000}},
		{Image: isolation.OwnedImage{ID: "c", Size: 9}, Keep: isolation.KeepNewestInSlot},
	}}
	var asked []string
	plan := func(byName map[string]isolation.ImagePrunePlan) func(context.Context, isolation.Runtime) (isolation.ImagePrunePlan, error) {
		return func(_ context.Context, rt isolation.Runtime) (isolation.ImagePrunePlan, error) {
			asked = append(asked, rt.Name())
			return byName[rt.Name()], nil
		}
	}
	both := []isolation.Runtime{isolation.Docker{}, isolation.Podman{}}

	clean := doctorCheckSupersededImages(context.Background(), both, plan(nil))
	assert.Equal(t, DoctorOK, clean.Status, clean.Detail)
	assert.Equal(t, []string{"docker", "podman"}, asked)

	found := doctorCheckSupersededImages(context.Background(), both, plan(map[string]isolation.ImagePrunePlan{"docker": superseded}))
	assert.Equal(t, DoctorWarn, found.Status)
	assert.Contains(t, found.Detail, "2 docker")
	assert.Contains(t, found.Detail, "2.5 GB")
	assert.Contains(t, found.Detail, "ctxloom container prune --apply")
	assert.NotContains(t, found.Detail, "podman")

	failing := doctorCheckSupersededImages(context.Background(), both, func(context.Context, isolation.Runtime) (isolation.ImagePrunePlan, error) {
		return isolation.ImagePrunePlan{}, errors.New("daemon gone")
	})
	assert.Equal(t, DoctorWarn, failing.Status)
	assert.Contains(t, failing.Detail, "daemon gone")
}
