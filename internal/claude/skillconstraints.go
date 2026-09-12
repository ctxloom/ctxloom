package claude

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Anthropic's Agent Skills hard constraints on a skill's frontmatter. They are
// enforced HERE, at the point a package is emitted for claude, and nowhere
// earlier: they are ONE vendor's acceptance rules, not a property of a skill
// package. A bundle's loader must carry every package it ships regardless of
// which engine can take it — a name claude refuses is still a valid package
// for an engine with different rules — so the loader parses frontmatter
// verbatim and each engine's writer refuses what it cannot deliver, loudly,
// naming the constraint. Nothing here silently truncates or skips.
const (
	// SkillNameMaxLen is the maximum length of a skill's frontmatter `name`.
	SkillNameMaxLen = 64
	// SkillDescriptionMaxLen is the maximum length of a skill's frontmatter
	// `description`.
	SkillDescriptionMaxLen = 1024
)

// skillNamePattern enforces "lowercase alphanumerics and hyphens only" on a
// skill's frontmatter name.
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// skillReservedWords may not appear as a substring of a skill's frontmatter
// name. The name has already passed skillNamePattern by the time this is
// consulted, so it is lowercase and a plain substring match is exact.
var skillReservedWords = []string{"anthropic", "claude"}

// checkSkillConstraints reports the first of Anthropic's hard constraints an
// export's frontmatter violates, as a precise, actionable error naming the
// field and the limit. Name and Description on the export ARE the frontmatter
// fields (buildSkillExports copies them verbatim), so this reads the export
// rather than re-parsing SKILL.md.
//
// The vendor's "name must match its directory" rule is satisfied by
// construction rather than checked: the writer materializes the package under
// its Name, so the directory claude scans can never disagree with the
// frontmatter inside it.
func checkSkillConstraints(s agent.SkillExport) error {
	if s.Name == "" {
		return fmt.Errorf("SKILL.md frontmatter is missing the required `name` field")
	}
	if len(s.Name) > SkillNameMaxLen {
		return fmt.Errorf("name %q is %d chars, exceeds the %d char limit", s.Name, len(s.Name), SkillNameMaxLen)
	}
	if !skillNamePattern.MatchString(s.Name) {
		return fmt.Errorf("name %q must be lowercase alphanumerics and hyphens only", s.Name)
	}
	for _, reserved := range skillReservedWords {
		if strings.Contains(s.Name, reserved) {
			return fmt.Errorf("name %q must not contain the reserved word %q", s.Name, reserved)
		}
	}
	if s.Description == "" {
		return fmt.Errorf("SKILL.md frontmatter is missing the required `description` field")
	}
	if len(s.Description) > SkillDescriptionMaxLen {
		return fmt.Errorf("description is %d chars, exceeds the %d char limit", len(s.Description), SkillDescriptionMaxLen)
	}
	return nil
}

// acceptedSkills returns the exports claude will emit: every enabled export
// that passes checkSkillConstraints. One that fails is refused with a warning
// naming the skill and the constraint, and dropped from the delivery — so its
// ledger-tracked copy from an earlier materialize is reverted exactly as a
// disabled skill's would be, never left on claude's surface claiming to be
// current. A DISABLED export is passed through unexamined: it was never going
// to be emitted, and a refusal warning for it would name a constraint nobody
// is about to hit.
//
// This runs BEFORE the shared writer's own path-safety pass on purpose: that
// pass also rejects an empty name, but as a path problem, and a skill missing
// its `name` field must be reported as the vendor constraint it is.
func acceptedSkills(skills []agent.SkillExport) []agent.SkillExport {
	out := make([]agent.SkillExport, 0, len(skills))
	for _, s := range skills {
		if s.Enabled {
			if err := checkSkillConstraints(s); err != nil {
				agent.Warn("refusing skill %q for claude: %v", s.Name, err)
				continue
			}
		}
		out = append(out, s)
	}
	return out
}
