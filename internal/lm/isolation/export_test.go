package isolation

// CodexInstallFragmentForTest exposes the codex image-install fragment to this
// package's EXTERNAL test package (isolation_test), which is where the
// cross-package adapter-name parity check has to live: internal/codex depends
// on this package, so only an external test package may import it without a
// cycle. Declared in an in-package test file, so it exists for tests only and
// adds nothing to the production surface.
var CodexInstallFragmentForTest = codexInstallFragment

// EnvCellWorkDirLiteral exposes the by-value copy of coord.EnvCellWorkDir this
// package keeps (see envCellWorkDir's doc for why it cannot import the
// original) so an EXTERNAL test package — which may import coord without
// creating the production cycle — can assert the two never drift.
const EnvCellWorkDirLiteral = envCellWorkDir
