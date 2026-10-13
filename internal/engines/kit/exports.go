package kit

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/schema"
)

// ExportDecoder is the decoder of one engine's per-item EXPORT BLOCK (the
// opaque bytes a bundle item carries under the engine's name): each block
// validated against the engine's schema (Definition.ExportSchema, compiled
// once on first use), then unmarshalled into T. nil is the absent block, T's
// zero value. A block the schema refuses is an error naming the engine: the
// item is withheld, never exported on a guess.
func ExportDecoder[T any](eng engine.Name, exportSchema []byte) func(raw json.RawMessage) (T, error) {
	compile := sync.OnceValues(func() (*schema.ConfigValidator, error) { return schema.NewValidatorFromSchema(exportSchema) })
	return func(raw json.RawMessage) (T, error) {
		var block T
		if raw == nil {
			return block, nil
		}
		v, err := compile()
		if err != nil {
			return block, fmt.Errorf("%s: export schema: %w", eng, err)
		}
		if err := v.ValidateBytes(raw); err != nil {
			return block, fmt.Errorf("%s: export block: %w", eng, err)
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return block, fmt.Errorf("%s: export block: %w", eng, err)
		}
		return block, nil
	}
}
