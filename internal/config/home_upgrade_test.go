package config

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// long-ice: with home < project layering, a stale ~/.ctxloom/config.yaml was
// upgraded in memory on EVERY load and never written back, so the home file
// never converged and the upgrade machinery redid the same work forever. The
// pending upgrade was visible but not persistable — CommitUpgrade only ever
// targeted the project (ambient) layer.
//
// CommitUpgrade already wrote to Pending.Path, so targeting home needed no new
// cross-cutting machinery: the home layer gets its own committer and rides the
// SAME consented prompt the project layer uses. Nothing here auto-writes a
// user's home config; the caller confirms first, and the prompt names the path.
func TestCommitHomeUpgrade(t *testing.T) {
	t.Run("persists the home layer and clears only its own pending", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		cfg := &Config{fs: fs}
		cfg.homePendingUpgrade = &upgrade.Pending{Path: "/home/u/.ctxloom/config.yaml", Data: []byte("version: 5\n")}
		cfg.pendingUpgrade = &upgrade.Pending{Path: "/proj/.ctxloom/config.yaml", Data: []byte("version: 5\n")}

		require.NoError(t, cfg.CommitHomeUpgrade())

		data, err := afero.ReadFile(fs, "/home/u/.ctxloom/config.yaml")
		require.NoError(t, err)
		assert.Equal(t, "version: 5\n", string(data), "the upgraded bytes must be written verbatim")
		assert.Nil(t, cfg.homePendingUpgrade, "a committed home upgrade must clear its pending")
		assert.NotNil(t, cfg.pendingUpgrade, "committing home must not touch the project layer's pending")

		exists, err := afero.Exists(fs, "/proj/.ctxloom/config.yaml")
		require.NoError(t, err)
		assert.False(t, exists, "committing home must not write the project file")
	})

	t.Run("nothing pending is a no-op", func(t *testing.T) {
		cfg := &Config{fs: afero.NewMemMapFs()}
		assert.NoError(t, cfg.CommitHomeUpgrade())
	})
}

// A pending upgrade carrying no bytes must never reach the user's file. The
// caller has just asked the user to consent to a REWRITE, so a nil return means
// that rewrite landed — and an empty payload lands as a zero-byte config.yaml
// over a file that was, until that moment, valid. internal/profiles already
// enforces this on its own committer; the config committer did not.
func TestCommitUpgrade_EmptyPayload_RefusesAndLeavesTheFileAlone(t *testing.T) {
	const path = "/proj/.ctxloom/config.yaml"

	for _, tc := range []struct {
		name    string
		pending *upgrade.Pending
		commit  func(c *Config) error
		field   func(c *Config) **upgrade.Pending
	}{
		{
			name:   "project layer",
			commit: (*Config).CommitUpgrade,
			field:  func(c *Config) **upgrade.Pending { return &c.pendingUpgrade },
		},
		{
			name:   "home layer",
			commit: (*Config).CommitHomeUpgrade,
			field:  func(c *Config) **upgrade.Pending { return &c.homePendingUpgrade },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			require.NoError(t, afero.WriteFile(fs, path, []byte("version: 6\nkept: yes\n"), 0o644))

			cfg := &Config{fs: fs}
			*tc.field(cfg) = &upgrade.Pending{Path: path, Data: nil, Applied: []string{"some-upgrade"}}

			err := tc.commit(cfg)
			require.Error(t, err, "an empty payload must fail loud, not report a successful rewrite")

			data, rerr := afero.ReadFile(fs, path)
			require.NoError(t, rerr)
			assert.Equal(t, "version: 6\nkept: yes\n", string(data), "the on-disk config must be untouched")
			assert.NotNil(t, *tc.field(cfg), "a refused commit must not clear the pending upgrade")
		})
	}
}
