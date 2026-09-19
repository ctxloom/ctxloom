package bundles

import "encoding/json"

// EngineBlocks are an item's per-engine exports: one OPAQUE block per engine
// name. This package carries a block as bytes and never reads inside one —
// the engine the key names decodes its own block against its ExportSchema,
// so a new engine needs no change here and a block for an engine this binary
// does not know is carried, not dropped.
type EngineBlocks map[string]json.RawMessage
