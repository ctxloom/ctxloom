package backends

import (
	"github.com/ctxloom/ctxloom/internal/lm/hosting"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// registerFixtures registers synthetic engines: each hosting record paired
// with a mock kind under its name, exactly as the composition root pairs
// the shipped ones.
func registerFixtures(hs ...hosting.Hosting) error {
	return Register(enginefixture.Registry(hs...), hs...)
}
