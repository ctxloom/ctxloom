package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// PushBundle publishes a single-file bundle through Publish and a
// directory-form one through PublishTree; the two transports differ, the
// OUTCOME they record must not. These tests hold both shapes to one
// expectation per field, so the recording can only stay equal by being one
// piece of code.

// pushShape is one bundle shape a push can take, and how to produce one.
type pushShape struct {
	name     string
	manifest func(t *testing.T, cfg *config.Config, single string) string
}

var pushShapes = []pushShape{
	{name: "single-file", manifest: func(_ *testing.T, _ *config.Config, single string) string { return single }},
	{name: "tree-form", manifest: func(t *testing.T, cfg *config.Config, _ string) string {
		return writeDirFormBundleFixture(t, cfg, "for-push-tree")
	}},
}

func TestPushBundle_EveryShape_RecordsTheDirectPushOutcome(t *testing.T) {
	for _, shape := range pushShapes {
		t.Run(shape.name, func(t *testing.T) {
			mock := &mockPublisher{returnCommitSHA: "sha0001"}
			cfg, single, mgr := pushTestSetup(t, mock)

			res, err := PushBundle(context.Background(), cfg, PushBundleRequest{
				Path:           shape.manifest(t, cfg, single),
				Remote:         "personal",
				PublishManager: mgr,
			})
			require.NoError(t, err)
			assert.Equal(t, "pushed", res.Status)
			assert.Equal(t, "sha0001", res.CommitSHA)
			assert.Empty(t, res.PRURL)
			assert.Empty(t, mock.createPRCalls)
		})
	}
}

func TestPushBundle_EveryShape_RecordsThePullRequestOutcome(t *testing.T) {
	for _, shape := range pushShapes {
		t.Run(shape.name, func(t *testing.T) {
			mock := &mockPublisher{
				returnCommitSHA: "sha0002",
				returnPRURL:     "https://github.com/example/personal-bundles/pull/7",
			}
			cfg, single, mgr := pushTestSetup(t, mock)

			res, err := PushBundle(context.Background(), cfg, PushBundleRequest{
				Path:           shape.manifest(t, cfg, single),
				Remote:         "personal",
				CreatePR:       true,
				PublishManager: mgr,
			})
			require.NoError(t, err)
			assert.Equal(t, "pr-created", res.Status)
			assert.Equal(t, "sha0002", res.CommitSHA)
			assert.Equal(t, "https://github.com/example/personal-bundles/pull/7", res.PRURL)
			require.Len(t, mock.createPRCalls, 1)
		})
	}
}

func TestPushBundle_EveryShape_SurfacesThePublisherError(t *testing.T) {
	for _, shape := range pushShapes {
		t.Run(shape.name, func(t *testing.T) {
			mock := &mockPublisher{returnErr: assert.AnError}
			cfg, single, mgr := pushTestSetup(t, mock)

			_, err := PushBundle(context.Background(), cfg, PushBundleRequest{
				Path:           shape.manifest(t, cfg, single),
				Remote:         "personal",
				PublishManager: mgr,
			})
			require.ErrorIs(t, err, assert.AnError)
		})
	}
}
