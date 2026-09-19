package config

import (
	kmaps "github.com/knadh/koanf/maps"

	"github.com/ctxloom/ctxloom/internal/adapters/configload/layerscope"
)

// scopePolicy is ctxloom's DefaultPolicy, resolved once — Policy is an
// immutable value built fresh by DefaultPolicy(), so every caller in this
// package shares the identical table rather than each rebuilding it.
var scopePolicy = layerscope.DefaultPolicy()

// DropLayerScopeViolations reports every layerscope violation layer's OWN
// decoded values commit, and removes each one from values in place via
// koanf/maps.Delete — never a bespoke recursive map walker — so the dropped
// key does not survive into what follows. One policy, two callers: the
// reader (adapters/configload) applies it per layer beside that layer's
// schema validation, so a key allowed at one layer but not another is caught
// at ITS OWN layer; saveLocked applies it at LayerProject over what a save
// is about to persist, because a value merged in from a lower layer must be
// stopped there too, not re-discovered the next time the file loads.
func DropLayerScopeViolations(layer layerscope.Layer, values map[string]any) []layerscope.Violation {
	violations := scopePolicy.Check(layer, values)
	for _, v := range violations {
		kmaps.Delete(values, v.Path)
	}
	return violations
}
