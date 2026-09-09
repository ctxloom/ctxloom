package backends

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// SupportsSkills reports whether backendName has an Agent Skills surface.
//
// It reads the SAME descriptor field SkillExportsFor gates on, deliberately: a
// caller that BRANCHES on skills support and then emits through SkillExportsFor
// must not be able to disagree with it about what that support is. A second
// source here — an ApproachTable lookup, say — would be a second policy, and
// the two would diverge the first time either changed.
func SupportsSkills(backendName string) bool {
	d, ok := lookup(backendName)
	return ok && d.skillExports != nil
}

// PremisedFragment is one fragment a STATIC assembly withheld: its qualified
// ref, the premise it applies under, and its already-gated body.
type PremisedFragment struct {
	Ref     string
	Premise string
	Content string
}

// PremisedFragmentSkills turns withheld premised fragments into Agent Skill
// packages, so an engine ctxloom will not be present to serve still gets
// progressive disclosure: the premise becomes the skill's description and sits
// in context, the body loads only when the model judges it applies.
//
// It takes the whole BATCH rather than one fragment because uniqueness is a
// property of the set. Two fragments landing on one package directory would
// silently overwrite — the payload-loss shape this project keeps hitting — so a
// collision is an error naming both refs, never a last-writer-wins.
func PremisedFragmentSkills(frags []PremisedFragment) ([]agent.SkillExport, error) {
	seen := make(map[string]string, len(frags))
	out := make([]agent.SkillExport, 0, len(frags))
	for _, f := range frags {
		name := skillNameForRef(f.Ref)
		if name == "" {
			return nil, fmt.Errorf("premised fragment %q: its ref yields no usable skill package name", f.Ref)
		}
		if prev, dup := seen[name]; dup {
			return nil, fmt.Errorf("premised fragments %q and %q both map to skill package %q — refusing to overwrite one with the other", prev, f.Ref, name)
		}
		seen[name] = f.Ref
		out = append(out, agent.SkillExport{
			Name:    name,
			Enabled: true,
			Files: []agent.PackageFile{{
				RelPath: "SKILL.md",
				Content: []byte(skillMarkdown(name, f.Premise, f.Content)),
				Mode:    0o644,
			}},
		})
	}
	return out, nil
}

// skillMarkdown renders the package's SKILL.md.
//
// The premise goes in the FRONTMATTER, not merely on SkillExport.Description:
// that struct field is write-only — no engine reads it — and the description an
// engine actually acts on travels inside these bytes. The frontmatter `name`
// must equal the package directory's basename or bundles.ParseSkillPackage
// rejects the package on load, so both come from skillNameForRef.
func skillMarkdown(name, premise, body string) string {
	var b strings.Builder
	b.WriteString("---\nname: ")
	b.WriteString(name)
	b.WriteString("\ndescription: ")
	b.WriteString(strings.ReplaceAll(strings.TrimSpace(premise), "\n", " "))
	b.WriteString("\n---\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String()
}

var skillNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9]+`)

// skillNameForRef derives a package directory name from a QUALIFIED fragment
// ref (bundle#fragments/name), per the 2026-09-08 ruling to qualify from the
// ref rather than flatten to the bare name.
//
// Qualifying is not cosmetic. A bare name is ambiguous — `general` is defined
// in seventeen code-review bundles in the default corpus — and a package
// directory is flat, so flattening to the short name would reintroduce exactly
// the collision the qualified ref exists to prevent, silently, at the emit
// boundary. A bundle ref may be a whole URL, so every character a directory
// name cannot safely carry collapses to a single dash; PremisedFragmentSkills
// still checks the RESULT for uniqueness, because sanitising can map two
// distinct refs onto one name.
func skillNameForRef(ref string) string {
	return strings.Trim(skillNameUnsafe.ReplaceAllString(ref, "-"), "-")
}
