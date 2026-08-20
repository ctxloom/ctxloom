package profiles

import (
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/keymatch"
	"github.com/ctxloom/ctxloom/resources"
)

// profileKeys is the set of keys a profile file may declare, read from the
// embedded profile schema rather than restated here.
//
// The schema is the single owner: TestArch_ProfileSchema_CoversEveryProfileField
// binds it to Profile's own yaml tags, so a field added to the struct without a
// schema entry fails that gate instead of becoming a key this function silently
// rejects.
var profileKeys = sync.OnceValue(func() map[string]bool {
	data, err := resources.GetProfileSchema()
	if err != nil {
		return nil
	}
	v, err := schema.NewValidatorFromSchema(data)
	if err != nil {
		return nil
	}
	keys := v.KnownKeys(nil)
	if len(keys) == 0 {
		return nil
	}
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
})

// warnUnknownProfileKeys reports any top-level key in a profile document that
// the schema does not declare.
//
// A typo'd key is the silent no-op this package is most exposed to: yaml.v3
// ignores what it cannot map, so `select_tagz:` yields a profile that selects
// nothing, loads with err=nil, and reports success at every surface. The inline
// `profiles:` block used to catch exactly this through the config schema's
// additionalProperties:false; retiring that arm left the only remaining way to
// author a profile with no such check at all.
//
// It WARNS rather than refusing, matching how the config path treats an unknown
// key: a profile carrying a stray key still loads with everything it does
// declare, because refusing would turn a typo into a launch failure and this is
// the arm every profile now takes.
//
// A nil key set (schema unreadable or empty) reports nothing. That is a
// deliberate fail-open: it means the check itself is broken, and a broken check
// must not start rejecting keys that are in fact fine.
func warnUnknownProfileKeys(path string, doc *yaml.Node) {
	known := profileKeys()
	if known == nil || doc == nil {
		return
	}
	root := doc
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return
	}
	var names []string
	for k := range known {
		names = append(names, k)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if known[key] {
			continue
		}
		msg := "unknown key `" + key + "` in " + path +
			": ctxloom does not know it, so it is IGNORED"
		if s := keymatch.Nearest(key, names); s != "" {
			msg += " — did you mean `" + s + "`?"
		}
		clidiag.Warn("ctxloom", "%s", msg)
	}
}
