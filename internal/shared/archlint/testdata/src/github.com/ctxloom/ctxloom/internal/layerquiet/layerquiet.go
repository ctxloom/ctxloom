package layerquiet

import "github.com/ctxloom/ctxloom/internal/layerlow" // want `^package internal/layerquiet imports github.com/ctxloom/ctxloom/internal/layerlow, which layering rule "t-rule" forbids`

// V uses the import.
const V = layerlow.V
