// Package deliverytest is the fixture half of the delivery port's tests: the
// file listing the tests compare deliveries by.
package deliverytest

import (
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"
)

// RelativeFiles lists every regular file under root, relative to it, in
// sorted order; nil when the root holds none.
func RelativeFiles(fsys afero.Fs, root string) []string {
	var out []string
	_ = afero.Walk(fsys, root, func(p string, info fs.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out
}
