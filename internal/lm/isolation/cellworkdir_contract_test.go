package isolation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// TestEnvCellWorkDir_MatchesTheCanonicalCoordConstant pins U063-F14. The
// isolation package copies coord.EnvCellWorkDir's VALUE as a literal because
// importing coord would cycle (coord -> lm/backends -> acp -> isolation), and
// nothing guarded the two staying equal: a rename on either side would leave
// the host+worktree runner keying its MCP discovery marker off a variable the
// shim never sets, silently disabling discovery with no error anywhere.
//
// This test lives in the EXTERNAL test package (isolation_test) precisely
// because that is the one place both packages may be imported together — an
// external test package's imports are XTestImports, not part of the
// production import graph, so this guard adds no edge and breaks no layering
// invariant. It must never be "fixed" by moving it into package isolation.
func TestEnvCellWorkDir_MatchesTheCanonicalCoordConstant(t *testing.T) {
	assert.Equal(t, coord.EnvCellWorkDir, isolation.EnvCellWorkDirLiteral,
		"isolation's by-value copy of the cell-workdir env var must track coord's canonical constant")
}
