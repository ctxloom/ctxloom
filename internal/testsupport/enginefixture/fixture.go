// Package enginefixture builds synthetic engine kinds for tests that stand
// an engine in front of the adapters by name: a mock kind under the name the
// test chooses, composed into a registry the test installs with engines.Use.
package enginefixture

import (
	"sync"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// Kind is a mock kind under name, adjusted by opts. It is Hosted, as every
// mock kind is.
func Kind(name string, opts ...mock.Option) engine.Engine {
	return mock.NewNamed(engine.Name(name), opts...)
}

// RegistryOf composes kinds into a registry; a duplicate name is a
// programming error in the test.
func RegistryOf(kinds ...engine.Engine) engine.Registry {
	reg, err := engine.NewRegistry(kinds...)
	if err != nil {
		panic("enginefixture: " + err.Error())
	}
	return reg
}

// Install stands kinds in front of every adapter that resolves an engine by
// name for the rest of the test: the composed registry (engines.Use) and
// the cells adapter's facts accessor over it, both restored at cleanup.
func Install(t testing.TB, kinds ...engine.Engine) engine.Registry {
	t.Helper()
	reg := RegistryOf(kinds...)
	restore := engines.Use(reg)
	restoreFacts := isolation.UseFacts(isolation.RegistryFacts{Registry: reg})
	t.Cleanup(func() { restoreFacts(); restore() })
	return reg
}

// MustComposeShipped composes the shipped engines the way the CLI root does
// — the registry, and the cells adapter's facts accessor over it — for a
// TestMain whose package resolves engines by name. Idempotent.
func MustComposeShipped() {
	engines.MustCompose()
	shippedOnce.Do(func() { isolation.UseFacts(isolation.RegistryFacts{Registry: engines.Registry()}) })
}

var shippedOnce sync.Once
