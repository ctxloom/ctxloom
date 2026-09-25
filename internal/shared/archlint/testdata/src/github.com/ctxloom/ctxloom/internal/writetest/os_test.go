package writetest

import "os"

type store struct{}

func (store) Create(string) error { return nil }

// realFS writes through os.* in a file that never imports afero: out of scope.
func realFS() {
	var fsys store
	_ = fsys.Create("x")
	_ = os.WriteFile("x", nil, 0o600)
}
