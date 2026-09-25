// Package docprod is the doc-comment rule's production fixture.
package docprod

// Prod is a doc comment pasted twice into the same place. Prod is a doc comment pasted twice into the same place.
func Prod() {} // want `doc comment for Prod restates its own opening`
