//go:build arch

package arch

// THE ONE-ENCODER RULE (plane-2 §6.6, §6.4).
//
// `<ctxloom-reminder>` frames are the ONE marker separating a runtime notice
// from the human speaking, in a channel that has no system role: everything
// arrives in the user turn, so an unmarked injection reads as the user's own
// words — i.e. as consent. That makes frame construction security-relevant.
//
// It used to be HAND-WRITTEN at each delivery site, and the two sites disagreed
// in exactly the way hand-written duplication always does: one interpolated a
// sender-controlled `kind` into the frame text (a child could mint what read as
// an approval prompt), and the other injected raw bytes with no frame at all.
// The fix is structural — frames are rendered ONLY by the generated `.XmlLike()`
// encoders, whose escaping and opt-in field rendering are enforced at BUILD
// time — and this gate is what keeps it structural. Without it, "use the
// generated encoder" is a code-review habit, and the next delivery site that
// builds a frame with fmt.Sprintf ships silently.
//
// The gate is deliberately a TEXT search over the whole module rather than a
// type-aware analysis: what must not exist is the literal tag anywhere outside
// generated code, whatever expression produces it. A type-aware rule would miss
// a frame assembled from fragments, which is precisely the shape the old bug
// had.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// generatedFrameEncoders is the ONE file allowed to construct a frame: the
// generator's output. It is listed by path rather than detected by the
// "Code generated" header, because a hand-written file can carry that header
// too — and the point of the gate is that the allowlist is a decision.
var generatedFrameEncoders = map[string]bool{
	"internal/agentcoord/xmllike_gen.go": true,
}

// frameDeclarers are the files allowed to mention the tag WITHOUT constructing
// a frame: the generator that emits the encoders, the convention's declaration
// in the delivered system-prompt/instruction text, and the tests that pin both.
// Each is listed with why, so a new entry has to be argued for.
var frameDeclarers = map[string]string{
	// Emits the encoders; holds the tag as the ReminderTag constant.
	"internal/agentcoord/mcpschema/xmllike.go": "the generator itself",
	// The generator's CLI: names the tag in its -xmllike-out flag help.
	"internal/agentcoord/mcpschema/gen/main.go": "the generator's entry point",
	// This gate.
	"tests/arch/reminder_frame_test.go": "this gate",
}

// TestArch_ReminderFramesAreConstructedOnlyByGeneratedCode fails if any
// non-test Go file outside the allowlist mentions the reminder tag.
func TestArch_ReminderFramesAreConstructedOnlyByGeneratedCode(t *testing.T) {
	root := moduleRoot(t)
	const tag = "<ctxloom-reminder"

	var offenders []string
	var sawGenerated bool
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "website", "dist", "man":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if generatedFrameEncoders[rel] {
			sawGenerated = true
			return nil
		}
		// protoc-gen-go copies every .proto comment into the generated Go
		// doc comment verbatim, and the reminder messages document the frame
		// they render. Those are COMMENTS in a file nobody edits — the .proto
		// is the source, and the .proto is not where a frame gets built.
		if strings.HasSuffix(rel, ".pb.go") {
			return nil
		}
		if _, ok := frameDeclarers[rel]; ok {
			return nil
		}
		// Test files may ASSERT on frames (the goldens have to name them);
		// they cannot deliver one into a turn stream.
		if strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), tag) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}

	// ANTI-VACUOUS: if the generated encoder file is gone, the gate above
	// passed by finding nothing at all — which is the failure it exists to
	// catch, wearing a green tick.
	if !sawGenerated {
		t.Fatal("internal/agentcoord/xmllike_gen.go is missing: run `just gen-mcp-schemas`. " +
			"Without it this gate proves nothing (no frames exist to be constructed anywhere)")
	}
	if len(offenders) > 0 {
		t.Errorf("these files construct a <ctxloom-reminder> frame by hand: %v\n"+
			"Frames are rendered ONLY by the generated .XmlLike() encoders, whose escaping and "+
			"opt-in field rendering are enforced at build time. Add a message to coordination.proto, "+
			"annotate its fields with (xml_role), and run `just gen-mcp-schemas`.", offenders)
	}
}

// TestArch_ReminderFrameAllowlistIsLive fails if an allowlist entry goes stale,
// so the exemptions cannot outlive the files they exempt. A stale allowlist is
// how a gate quietly stops gating.
func TestArch_ReminderFrameAllowlistIsLive(t *testing.T) {
	root := moduleRoot(t)
	for rel := range generatedFrameEncoders {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("allowlisted generated file %s does not exist: %v", rel, err)
		}
	}
	for rel, why := range frameDeclarers {
		path := filepath.Join(root, rel)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("frameDeclarers lists %s (%s) but it does not exist: %v", rel, why, err)
			continue
		}
		if !strings.Contains(string(b), "ctxloom-reminder") {
			t.Errorf("frameDeclarers lists %s (%s) but it no longer mentions the tag — drop the entry", rel, why)
		}
	}
}
