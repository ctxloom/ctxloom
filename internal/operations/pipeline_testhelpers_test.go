package operations

import (
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
)

// opPipe wraps a test reader in an UNGATED process stage at cfg's configured
// form. It is the shape an injected reader used to imply: gating was a property
// of how the reader had been CONSTRUCTED, and these readers are constructed
// without a gate, so an ungated pipeline preserves exactly what they meant.
// Form comes from cfg because that is where the exposure surfaces read it —
// never from the reader.
func opPipe(cfg *config.Config, l *bundles.Loader) *bundles.Pipeline {
	return bundles.NewPipeline(l, nil, cfgPreferDistilled(cfg))
}
