package trust

import "fmt"

// ItemRef mints the canonical "<source>#<kind>/<item>" reference an item's
// trust key is built from, given the bundle-level ref its reader already
// stamped.
//
// It returns the fallback string ALONGSIDE the error rather than instead of it.
// The caller needs both: the string, because an item must key somewhere stable
// even when it cannot be addressed, and the error, because an unaddressable
// item is WITHHELD from delivery and a caller that cannot see the failure
// reports content vanishing as success. Every caller is expected to report the
// error; none may treat the returned string as a successful mint.
//
// The fallback carries kind and item, not src alone. WithItem fails BEFORE they
// land on the BundleRef, so a %#v of src is identical for every item of one
// unaddressable bundle — without them, a fragment and a command sharing a
// source degrade to the SAME key, only one is ever tallied, and the other's
// withholding looks like it never happened. It also cannot be confused with a
// real Identity(), which never starts with the literal scheme prefix followed
// by "unaddressable:".
func ItemRef(src BundleRef, kind ItemKind, item string) (string, error) {
	br, err := src.WithItem(kind, item)
	if err != nil {
		return fmt.Sprintf("ctxloom+unaddressable:%#v#%s/%s", src, kind.Dir(), item), err
	}
	return br.String(), nil
}
