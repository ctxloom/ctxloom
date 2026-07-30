package isolation

// EnvCellWorkDirLiteral exposes the by-value copy of coord.EnvCellWorkDir this
// package keeps (see envCellWorkDir's doc for why it cannot import the
// original) so an EXTERNAL test package — which may import coord without
// creating the production cycle — can assert the two never drift.
const EnvCellWorkDirLiteral = envCellWorkDir
