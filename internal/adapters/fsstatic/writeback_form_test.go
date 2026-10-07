package fsstatic

import (
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/testsupport/yamlform"
)

// TestClaimsRecordSaveIsWriteBackForm: a claims record is saved in the
// encoding an upgrade write-back would give it.
func TestClaimsRecordSaveIsWriteBackForm(t *testing.T) {
	fs := withUserFile(t)
	c := newRecords(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))

	saved := read(t, fs, filepath.Join(claimsDir, confpatch.RecordPrefix(mcpTarget)+claimsSuffix))
	yamlform.RequireWriteBackForm(t, []byte(saved))
}
