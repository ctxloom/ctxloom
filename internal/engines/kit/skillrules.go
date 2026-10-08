package kit

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The Agent Skills frontmatter standard: the constraints a skill's SKILL.md
// frontmatter meets for an engine that loads the standard. They are enforced
// where a package is emitted for an engine, never earlier: a bundle carries
// every package it ships whichever engine can take it, and each engine's
// writer refuses, loudly and naming the constraint, what it cannot deliver.
// Only the reserved words differ per engine.
const (
	// SkillNameMaxLen is the maximum length of a skill's frontmatter `name`.
	SkillNameMaxLen = 64
	// SkillDescriptionMaxLen is the maximum length of a skill's frontmatter
	// `description`.
	SkillDescriptionMaxLen = 1024
)

// skillNamePattern is "lowercase alphanumerics and hyphens only".
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// CheckAgentSkill reports the first constraint s's frontmatter violates, as
// an error naming the field and the limit; reserved are words the name may
// not contain anywhere. Name and Description on the export ARE the
// frontmatter fields. The "name matches its directory" rule holds by
// construction: the writer places the package under its Name.
func CheckAgentSkill(s agent.SkillExport, reserved ...string) error {
	if s.Name == "" {
		return fmt.Errorf("SKILL.md frontmatter is missing the required `name` field")
	}
	if len(s.Name) > SkillNameMaxLen {
		return fmt.Errorf("name %q is %d chars, exceeds the %d char limit", s.Name, len(s.Name), SkillNameMaxLen)
	}
	if !skillNamePattern.MatchString(s.Name) {
		return fmt.Errorf("name %q must be lowercase alphanumerics and hyphens only", s.Name)
	}
	// The name is lowercase by now, so a plain substring match is exact.
	for _, word := range reserved {
		if strings.Contains(s.Name, word) {
			return fmt.Errorf("name %q must not contain the reserved word %q", s.Name, word)
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

// AgentSkillRules is the accept filter (DeliverSkills) of an engine that
// loads the Agent Skills standard: every enabled export that passes
// CheckAgentSkill with the engine's reserved words. One that fails is
// refused with a warning naming the skill, the engine and the constraint,
// and dropped, so it is not declared and the static writer releases an
// earlier copy as it would a disabled skill's. A disabled export passes
// unexamined: it was never going to be emitted.
//
// It runs before the shared writer's path-safety pass on purpose: that pass
// also rejects an empty name, but as a path problem, and a missing `name`
// field is reported as the constraint it is.
func AgentSkillRules(eng engine.Name, reserved ...string) func([]agent.SkillExport) []agent.SkillExport {
	return func(skills []agent.SkillExport) []agent.SkillExport {
		out := make([]agent.SkillExport, 0, len(skills))
		for _, s := range skills {
			if s.Enabled {
				if err := CheckAgentSkill(s, reserved...); err != nil {
					clidiag.Warn("ctxloom", "refusing skill %q for %s: %v", s.Name, eng, err)
					continue
				}
			}
			out = append(out, s)
		}
		return out
	}
}
