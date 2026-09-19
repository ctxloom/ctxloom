package configload

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// updater is a test's write path: a throwaway config.Owner over the given
// options, whose Update writes through to the file exactly as the process's
// owner would.
type updater struct {
	t     *testing.T
	owner *config.Owner
}

func newUpdater(t *testing.T, opts ...Option) *updater {
	t.Helper()
	src, err := New(nil, nil, opts...)
	require.NoError(t, err)
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	return &updater{t: t, owner: owner}
}

func (u *updater) Update(fn func(*config.Draft) error) error {
	_, err := u.owner.Update(context.Background(), fn)
	return err
}
