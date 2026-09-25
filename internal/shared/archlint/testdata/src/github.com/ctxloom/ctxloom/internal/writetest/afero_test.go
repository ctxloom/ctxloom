package writetest

import (
	"os"

	"github.com/spf13/afero"
)

var seeded = afero.WriteFile(afero.NewMemMapFs(), "seed", nil, 0o600) // want `<package-level> calls afero.WriteFile directly — raw filesystem writes must route through testsupport`

func seedFixture() {
	fs := afero.NewMemMapFs()
	_, _ = fs.Create("x")             // want `seedFixture calls \(afero.Fs\).Create directly`
	_ = os.WriteFile("x", nil, 0o600) // real-filesystem writes are not the test arm's subject
}
