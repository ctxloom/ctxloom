package config

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
)

// TestGetProfileLoader_ReturnsAFreshLoader pins the property that lets the
// profiles port get away with declaring no concurrency contract: a *Loader
// accumulates its schema-upgrade ledger unsynchronised as it reads, which is
// safe only while no two callers ever hold the same one. Memoising this
// accessor — an obvious-looking optimisation, since it rebuilds the seed each
// call — would silently turn that into shared mutable state across every
// goroutine that resolves a profile.
func TestGetProfileLoader_ReturnsAFreshLoader(t *testing.T) {
	cfg := NewFixture(Fixture{AppPaths: []string{"/app"}})
	cfg.SetFS(afero.NewMemMapFs())

	first := cfg.GetProfileLoader()
	second := cfg.GetProfileLoader()

	assert.NotNil(t, first)
	assert.NotSame(t, first, second, "each call must build its own loader")
}
