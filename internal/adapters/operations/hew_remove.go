package operations

import (
	"fmt"
	"path/filepath"

	hew "github.com/benjaminabbitt/hew/go"
)

// removePointers takes each RFC 6901 pointer out of data, a document in the
// format target's name declares, byte-preservingly through hew: ordering and
// whitespace outside the removed values are the user's and stay put.
//
// It is deliberately RECORDLESS, unlike confpatch's applications: taking an
// entry a previous ctxloom left behind back out of a file is not one of
// ctxloom's own writes, so there is nothing to reverse later. Pointers apply in
// the order given, each against the document the previous one left, so a
// caller removing several elements of one array passes them highest index
// first.
func removePointers(target string, data []byte, pointers []string) ([]byte, error) {
	if len(pointers) == 0 {
		return data, nil
	}
	format, ok := hew.DetectFormat(filepath.Base(target))
	if !ok {
		return nil, fmt.Errorf("hew does not recognize %s's format from its name, so it cannot be edited byte-preservingly", target)
	}
	doc, err := hew.OpenBytes(target, data, hew.As(format))
	if err != nil {
		return nil, fmt.Errorf("open %s for hew: %w", target, err)
	}
	for _, ptr := range pointers {
		p, err := hew.ParsePathIn(format, ptr)
		if err != nil {
			return nil, fmt.Errorf("%s: pointer %q: %w", target, ptr, err)
		}
		doc.AtPath(p).Remove()
	}
	out, err := doc.Bytes()
	if err != nil {
		return nil, fmt.Errorf("remove from %s: %w", target, err)
	}
	return out, nil
}
