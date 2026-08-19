// Package policy decides which canonical tool-content blocks reach a
// transcript.
//
// It sits ABOVE every vendor adapter, and that separation is the whole point.
// An adapter (internal/transcript/vendorreader/{claude,codex,kiro,...})
// canonicalizes its vendor's format into ctxloom's shape TOTALLY and without
// judgement — every element becomes a block, with Raw carrying the vendor
// bytes verbatim. Only then does this package drop anything.
//
// Why the two are separate layers rather than one pass: a parser that also
// filters cannot tell you what it discarded, so its losses are invisible and
// per-engine. Measured before this package existed, the claude adapter kept
// only type:"text" elements of a tool_result and reported the rest as having
// "no canonical representation" — 677 tool_reference + 1 image elements gone
// across 1030 transcripts, and 385 tool_result entries flattened to
// completely EMPTY across 145 files, each reading as "this tool returned
// nothing". With the layers split there is exactly one file to read to know
// what a transcript omits, and it reads the same for every engine.
//
// A RULE MAY NEVER NAME A VENDOR FIELD. Rules see canonical agent.Kind*
// values only. A rule that reaches for a claude-shaped key is a defect: it
// silently does nothing for codex and kiro, which is the failure this
// boundary exists to prevent.
//
// The policy is HARD-CODED, not a config surface. Revisit when a second
// opinion about it actually exists.
package policy

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Reasons a block is withheld. Recorded on the marker so a reader can tell
// WHY, not merely that something is missing.
const (
	// ReasonRedundant: the same information is already recorded in another
	// form on the same entry, so withholding it loses nothing.
	ReasonRedundant = "redundant-with-tool-output"
	// ReasonBloat: the element is simply enormous. Withholding it DOES lose
	// information, and that cost was accepted knowingly.
	ReasonBloat = "bloat"
)

// Rule reports whether a block is recorded, and names the reason when not.
// It sees CANONICAL kinds only; a rule naming a vendor field is a defect.
type Rule func(agent.ToolContentBlock) (keep bool, reason string)

// Policy is an ordered rule set. The first rule to reject a block wins.
type Policy struct{ rules []Rule }

// New builds a Policy from rules, in evaluation order. Exported so a test can
// drive Apply with a rule set it controls rather than reaching into Default's
// decisions.
func New(rules ...Rule) Policy { return Policy{rules: rules} }

// Default is the policy every transcript writer uses.
//
//	process_output  exclude  redundant-with-tool-output
//	file_snapshot   exclude  bloat
//	everything else keep
//
// process_output is free to drop: measured over the last 40 sessions of this
// project, Bash's structured stdout is 2.70 MB against the 2.36 MB of
// tool_result text the transcript already records — substantially the same
// bytes twice.
//
// file_snapshot is NOT free, and was excluded anyway with the cost accepted
// knowingly: Edit carries 2.34 MB of whole-file pre-edit
// images against 0.09 MB of result text, duplicated nowhere. It snapshots the
// entire file on every edit.
//
// Note what is deliberately NOT excluded: a Read's file content, an agent
// result, a tool catalogue, a question's answers. Those are the tool's actual
// output, and the point of this package is that dropping them would be a
// decision written down here rather than an accident in a parser.
func Default() Policy {
	return New(
		excludeKind(agent.KindProcessOutput, ReasonRedundant),
		excludeKind(agent.KindFileSnapshot, ReasonBloat),
	)
}

// excludeKind builds the one rule shape the default policy needs: reject
// exactly this canonical kind, with this reason.
func excludeKind(kind, reason string) Rule {
	return func(b agent.ToolContentBlock) (bool, string) {
		if b.Kind == kind {
			return false, reason
		}
		return true, ""
	}
}

// Excluded builds the marker that REPLACES a withheld block. n is the size in
// bytes of what was withheld.
//
// The marker is not optional and must never be omitted in favour of simply
// dropping the element: silent omission would rebuild, on purpose, the exact
// defect this package was written to fix. A reader must always be able to
// distinguish "policy withheld this" from "the tool returned nothing".
//
// It uses only existing ToolContentBlock fields, so this adds no struct
// field, no proto change, and no SchemaVersion bump.
func Excluded(kind, reason string, n int) agent.ToolContentBlock {
	return agent.ToolContentBlock{
		Kind: agent.KindExcluded,
		Text: fmt.Sprintf("%s withheld (%d bytes)", kind, n),
		Raw: []byte(fmt.Sprintf(
			`{"excluded_kind":%q,"bytes":%d,"rule":%q}`, kind, n, reason)),
	}
}

// ApplyBlocks returns blocks with every withheld one replaced by its marker.
// Survivors pass through untouched. Returns the input slice unchanged when
// nothing is withheld, so a read that filters nothing allocates nothing.
func (p Policy) ApplyBlocks(blocks []agent.ToolContentBlock) []agent.ToolContentBlock {
	if len(blocks) == 0 {
		return blocks
	}
	changed := false
	for _, b := range blocks {
		if keep, _ := p.decide(b); !keep {
			changed = true
			break
		}
	}
	if !changed {
		return blocks
	}
	out := make([]agent.ToolContentBlock, 0, len(blocks))
	for _, b := range blocks {
		keep, reason := p.decide(b)
		if keep {
			out = append(out, b)
			continue
		}
		out = append(out, Excluded(b.Kind, reason, len(b.Raw)))
	}
	return out
}

// ApplyEntry returns e with its tool content filtered. The entry is returned
// BY VALUE and never mutated in place, so a caller holding the original —
// including the on-disk records a reader just parsed — keeps the total form.
func (p Policy) ApplyEntry(e agent.SessionEntry) agent.SessionEntry {
	e.ToolContent = p.ApplyBlocks(e.ToolContent)
	return e
}

// ApplySession returns a COPY of s with every entry filtered.
//
// A copy, not an in-place rewrite, because this runs on the read path: the
// session a reader parsed is the total, on-disk truth, and a consumer that
// filtered it for its own purposes must not have altered what the next
// consumer sees. Nil in, nil out.
func (p Policy) ApplySession(s *agent.Session) *agent.Session {
	if s == nil {
		return nil
	}
	out := *s
	out.Entries = make([]agent.SessionEntry, 0, len(s.Entries))
	for _, e := range s.Entries {
		out.Entries = append(out.Entries, p.ApplyEntry(e))
	}
	return &out
}

// decide runs the rules in order; the first rejection wins.
func (p Policy) decide(b agent.ToolContentBlock) (bool, string) {
	for _, r := range p.rules {
		if keep, reason := r(b); !keep {
			return false, reason
		}
	}
	return true, ""
}
