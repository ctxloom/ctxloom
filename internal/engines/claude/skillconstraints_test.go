package claude

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// skillExport builds an enabled single-file skill export whose SKILL.md
// frontmatter mirrors name/description, the shape buildSkillExports hands
// claude's skills approach.
func skillExport(name, description string) agent.SkillExport {
	return agent.SkillExport{
		Name:        name,
		Description: description,
		Enabled:     true,
		Files: []agent.PackageFile{{
			RelPath: "SKILL.md",
			Content: []byte("---\nname: " + name + "\ndescription: " + description + "\n---\n\nBody\n"),
			Mode:    0644,
		}},
	}
}

// captureWarnings routes clidiag warnings into a buffer for the test's life.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	t.Cleanup(clidiag.SetSink(&buf))
	return &buf
}

// TestDeliverSkills_VendorInvalidSkillIsRefusedNamingTheConstraint pins the
// emit boundary: a skill whose frontmatter violates one of Anthropic's hard
// constraints is refused HERE — never written, never delivered — with a
// warning that names the skill and the constraint it broke, while a valid
// sibling in the same delivery still lands.
func TestDeliverSkills_VendorInvalidSkillIsRefusedNamingTheConstraint(t *testing.T) {
	cases := []struct {
		label      string
		skill      agent.SkillExport
		constraint string
	}{
		{"missing name", skillExport("", "d"), "`name`"},
		{"name too long", skillExport(strings.Repeat("a", SkillNameMaxLen+1), "d"), strconv.Itoa(SkillNameMaxLen)},
		{"name not lowercase-hyphen", skillExport("Bad_Name", "d"), "lowercase"},
		{"name carries a reserved word", skillExport("claude-helper", "d"), "reserved"},
		{"missing description", skillExport("nodesc", ""), "`description`"},
		{"description too long", skillExport("longdesc", strings.Repeat("d", SkillDescriptionMaxLen+1)), strconv.Itoa(SkillDescriptionMaxLen)},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			dir := t.TempDir()
			warnings := captureWarnings(t)

			require.NoError(t, deliverProjectSkills(t, dir, []agent.SkillExport{tc.skill, skillExport("sibling", "fine")}))

			skillsDir := filepath.Join(dir, ConfigDirName, SkillsDirName)
			assert.FileExists(t, filepath.Join(skillsDir, "sibling", "SKILL.md"),
				"a refused skill must not take a valid sibling down with it")
			if tc.skill.Name != "" {
				assert.NoDirExists(t, filepath.Join(skillsDir, tc.skill.Name))
			}
			assert.Contains(t, warnings.String(), tc.constraint, "the refusal must name the constraint")
			assert.Contains(t, warnings.String(), strconv.Quote(tc.skill.Name), "the refusal must name the skill")
		})
	}
}

// TestDeliverSkills_RefusedSkillIsNotDeclared pins that a skill which was
// emitted once and later edited into a vendor-invalid state does not linger
// on claude's surface: the delivery refuses it (loudly) and does not declare
// it, so the static writer releases its earlier copy exactly as it would a
// disabled skill's.
func TestDeliverSkills_RefusedSkillIsNotDeclared(t *testing.T) {
	def := claudeDef(t)
	start, _, home := hostStart(t)
	skill := func(name, description string) engine.SkillExport {
		e := skillExport(name, description)
		return engine.SkillExport{Name: e.Name, Description: e.Description, Enabled: true,
			Files: []engine.SkillFile{{Path: "SKILL.md", Bytes: e.Files[0].Content, Mode: 0o644}}}
	}

	warnings := captureWarnings(t)
	d, err := def.Skills.DeliverSkills(start, present.RootSessionHome, engine.SkillsInputs{Skills: []engine.SkillExport{
		skill("fine", "fine"),
		skill("humanize", strings.Repeat("d", SkillDescriptionMaxLen+1)),
	}}, safefs.New())
	require.NoError(t, err)

	assert.Equal(t, []string{filepath.Join(home, SkillsDirName, "fine", "SKILL.md")}, d.Files,
		"a refused skill is not declared, so its earlier copy is released")
	assert.Contains(t, warnings.String(), strconv.Itoa(SkillDescriptionMaxLen))
}

// TestDeliverSkills_DisabledInvalidSkillIsNotRefused pins that a disabled
// export is not held to the vendor constraints: it is not emitted either way,
// and a refusal warning for it would be noise naming a rule nobody hit.
func TestDeliverSkills_DisabledInvalidSkillIsNotRefused(t *testing.T) {
	dir := t.TempDir()
	warnings := captureWarnings(t)
	off := skillExport("Bad_Name", "")
	off.Enabled = false

	require.NoError(t, deliverProjectSkills(t, dir, []agent.SkillExport{off}))

	assert.Empty(t, warnings.String())
}

// TestDeliverSkills_ValidSkillPassesWithoutWarning is the control: a skill
// at the exact limits emits with no refusal.
func TestDeliverSkills_ValidSkillPassesWithoutWarning(t *testing.T) {
	dir := t.TempDir()
	warnings := captureWarnings(t)
	name := strings.Repeat("a", SkillNameMaxLen)

	require.NoError(t, deliverProjectSkills(t, dir, []agent.SkillExport{skillExport(name, strings.Repeat("d", SkillDescriptionMaxLen))}))

	_, err := os.Stat(filepath.Join(dir, ConfigDirName, SkillsDirName, name, "SKILL.md"))
	require.NoError(t, err)
	assert.Empty(t, warnings.String())
}

// checkSkillConstraints boundary tests — pin the EXACT `>` limits so a
// CONDITIONALS_BOUNDARY mutant (`>` becoming `>=`) is caught: a name or
// description exactly AT the limit is accepted, one character over is not.

func TestCheckSkillConstraints_NameLengthBoundary(t *testing.T) {
	exact := strings.Repeat("a", SkillNameMaxLen)
	assert.NoError(t, checkSkillConstraints(skillExport(exact, "d")),
		"a name exactly at the %d char limit must be accepted", SkillNameMaxLen)

	over := strings.Repeat("a", SkillNameMaxLen+1)
	err := checkSkillConstraints(skillExport(over, "d"))
	require.Error(t, err, "a name one char OVER the limit must be refused")
	assert.Contains(t, err.Error(), strconv.Itoa(SkillNameMaxLen))
}

func TestCheckSkillConstraints_DescriptionLengthBoundary(t *testing.T) {
	exact := strings.Repeat("d", SkillDescriptionMaxLen)
	assert.NoError(t, checkSkillConstraints(skillExport("validname", exact)),
		"a description exactly at the %d char limit must be accepted", SkillDescriptionMaxLen)

	over := strings.Repeat("d", SkillDescriptionMaxLen+1)
	err := checkSkillConstraints(skillExport("validname", over))
	require.Error(t, err, "a description one char OVER the limit must be refused")
	assert.Contains(t, err.Error(), strconv.Itoa(SkillDescriptionMaxLen))
}

// TestCheckSkillConstraints_ReservedWordIsASubstringMatch pins that the
// reserved-word rule matches anywhere in the name, not only as a whole
// hyphenated segment, and that a near-miss passes.
func TestCheckSkillConstraints_ReservedWordIsASubstringMatch(t *testing.T) {
	for _, name := range []string{"my-anthropic-tool", "helper-for-claude", "xclaudex"} {
		err := checkSkillConstraints(skillExport(name, "d"))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "reserved")
	}
	assert.NoError(t, checkSkillConstraints(skillExport("clause-parser", "d")),
		"a near-miss that does not contain the reserved word must pass")
}
