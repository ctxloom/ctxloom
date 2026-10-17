package plans

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// IsPlanFile matches project plan documents: a basename BEGINNING with "plan"
// or "current_plan", or the repo-relative path docs/<name>-plan.md.
func TestIsPlanFile(t *testing.T) {
	for _, p := range []string{"PLAN.md", "current_plan.md", "plan-of-attack.md", "docs/auth-plan.md"} {
		assert.True(t, IsPlanFile(p), "%s should be a plan file", p)
	}
	for _, p := range []string{"README.md", "main.go", "docs/notes.md"} {
		assert.False(t, IsPlanFile(p), "%s should not be a plan file", p)
	}
}

// TestIsPlanFile_UnmatchedShapes is characterization, not endorsement. Each
// path below is one a reader of the old doc comment ("*plan*.md,
// docs/*-plan.md") would expect to match, and none of them do. Naming them
// here keeps the gap visible so that closing it is a decision someone makes,
// rather than a regex quietly widening under a hook that rewrites files in
// the user's home directory.
func TestIsPlanFile_UnmatchedShapes(t *testing.T) {
	// The basename must BEGIN with "plan": "*plan*.md" overstates the regex.
	for _, p := range []string{"design-plan.md", "my_plan.md", "docs/the-plan-doc.md"} {
		assert.False(t, IsPlanFile(p), "%s: the regex anchors plan at the basename start", p)
	}

	// The docs leg is anchored at the start of the string rather than at a
	// path separator, and the stamp-plan hook is handed an ABSOLUTE file_path
	// by the host engine, so that leg never fires in production.
	assert.True(t, IsPlanFile("docs/auth-plan.md"))
	assert.False(t, IsPlanFile("/home/u/proj/docs/auth-plan.md"),
		"the docs leg matches only a repo-relative path")

	// The session plan-document convention, <output dir>/<name>.plan.md.
	// internal/shared/plans reads a `sessions:` list out of these and its doc
	// names StampPlanFile as the writer of that list; this regex is what stands
	// between the hook and those files.
	for _, p := range []string{"v1-removal.plan.md", "/home/u/.ctxloom/sessions/vital-deaf-stunt/v1-removal.plan.md"} {
		assert.False(t, IsPlanFile(p),
			"%s: session plan documents are not matched today — see IsPlanFile's doc comment", p)
	}
}
