package trust

import "fmt"

// ItemRef mints the canonical "<source>#<kind>/<item>" reference an item's
// trust key is built from, given the bundle-level ref its reader already
// stamped.
//
// A source that cannot be addressed yields an ERROR and no string. There is no
// placeholder identity: a reference is either the canonical address of an item
// a grant can key on, or it does not exist. Minting a stand-in would push a
// parse failure through the IDENTITY channel and leave a later stage to refuse
// it, which splits one validation across two layers and keys the refusal on a
// string no human can act on.
//
// The caller's obligation is therefore to SKIP the one item this names and
// carry on: an item whose source cannot be addressed costs itself, never the
// rest of the assembly.
func ItemRef(src BundleRef, kind ItemKind, item string) (string, error) {
	br, err := src.WithItem(kind, item)
	if err != nil {
		return "", fmt.Errorf("cannot address %s/%s: its bundle has no addressable source: %w", kind.Dir(), item, err)
	}
	return br.String(), nil
}
