package archlint

import (
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// ReminderTagText is the literal that must not appear outside generated code.
const ReminderTagText = "<ctxloom-reminder"

// generatedFrameEncoders is the one file allowed to construct a frame: the
// generator's output. It is listed by path rather than detected by a
// "Code generated" header, because a hand-written file can carry that header
// too — the allowlist is a decision.
var generatedFrameEncoders = map[string]bool{
	"internal/agentcoord/xmllike_gen.go": true,
}

// frameDeclarers are the files allowed to mention the tag WITHOUT
// constructing a frame: the generator that emits the encoders, and its CLI.
var frameDeclarers = map[string]string{
	"internal/agentcoord/mcpschema/xmllike.go":  "the generator itself",
	"internal/agentcoord/mcpschema/gen/main.go": "the generator's entry point",
	"internal/archlint/reminderframe.go":        "this rule, which must name the tag to search for it",
}

// ReminderFrameAnalyzer enforces that <ctxloom-reminder> frames are rendered
// only by the generated XmlLike encoders.
//
// The frame is the ONE marker separating a runtime notice from the human
// speaking, in a channel that has no system role: everything arrives in the
// user turn, so an unmarked injection reads as the user's own words — as
// consent. That makes frame construction security-relevant. Hand-written
// frames drifted once already, one interpolating a sender-controlled value
// into the frame text so a child could mint what read as an approval prompt.
//
// The check is a TEXT search rather than a type-aware analysis on purpose:
// what must not exist is the literal tag, whatever expression produces it. A
// type-aware rule would miss a frame assembled from fragments, which is
// precisely the shape the original defect had.
var ReminderFrameAnalyzer = &analysis.Analyzer{
	Name: "archreminderframe",
	Doc:  "<ctxloom-reminder> frames may be constructed only by the generated encoders",
	Run:  runReminderFrame,
}

func runReminderFrame(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	if PkgDir(pass) == "" {
		return nil, nil
	}
	for _, f := range ProdFiles(pass) {
		rel := FileRel(pass, f)
		if generatedFrameEncoders[rel] {
			continue
		}
		// protoc-gen-go copies every .proto comment into a Go doc comment
		// verbatim, and the reminder messages document the frame they render.
		// A comment in a file nobody edits is not where a frame gets built.
		if strings.HasSuffix(rel, ".pb.go") {
			continue
		}
		_, declarer := frameDeclarers[rel]
		mentions := fileMentionsTag(pass, f)
		if mentions && !declarer {
			pass.Reportf(f.Package,
				"%s constructs a <ctxloom-reminder> frame by hand — frames are rendered ONLY by the "+
					"generated .XmlLike() encoders, whose escaping and opt-in field rendering are "+
					"enforced at build time. Add a message to coordination.proto, annotate its fields "+
					"with (xml_role), and run `just gen-mcp-schemas`.", rel)
		}
		if declarer && !mentions && allowlistLivenessEnabled() {
			pass.Reportf(f.Package,
				"frameDeclarers lists %q but it no longer mentions the tag — drop the entry", rel)
		}
	}
	return nil, nil
}

// fileMentionsTag reports whether the tag appears anywhere in the file's
// source, including comments and fragments of a composed string.
func fileMentionsTag(pass *analysis.Pass, f *ast.File) bool {
	name := pass.Fset.Position(f.Pos()).Filename
	b, err := pass.ReadFile(name)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), ReminderTagText)
}
