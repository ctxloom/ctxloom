package cli

import (
	"strings"
	"testing"
)

// TestBuildDistillMessage_NameIsAttributeNotHeading guards the regression where
// the item name was injected as a `# name` markdown heading into the compressor
// input, which the model echoed on top of the content's own H1 — doubling the
// title in the distilled output.
func TestBuildDistillMessage_NameIsAttributeNotHeading(t *testing.T) {
	content := "# Code Review — Conduct\n\nBody."
	msg := buildDistillMessage("COMPRESS THIS", "", "conduct", content)

	// Name rides as a tag attribute.
	if !strings.Contains(msg, `<content_to_compress name="conduct">`) {
		t.Errorf("expected name as a tag attribute, got:\n%s", msg)
	}
	// Name must NOT appear as an injected markdown heading (the regression).
	if strings.Contains(msg, "\n# conduct\n") {
		t.Errorf("item name leaked as a markdown heading (doubled-heading regression):\n%s", msg)
	}
	// The content's own heading immediately follows the opening tag — nothing
	// injected between them.
	if !strings.Contains(msg, "<content_to_compress name=\"conduct\">\n# Code Review — Conduct") {
		t.Errorf("content's own heading not preserved directly after the tag:\n%s", msg)
	}
	// The distill prompt leads the message (compressor framing).
	if !strings.HasPrefix(msg, "COMPRESS THIS\n\n") {
		t.Errorf("distill prompt should lead the message:\n%s", msg)
	}
}

// TestBuildDistillMessage_SiblingContext checks the optional bundle-context block
// is wrapped when sibling context is supplied, and omitted when it is not.
func TestBuildDistillMessage_SiblingContext(t *testing.T) {
	with := buildDistillMessage("P", "sibling info", "frag", "body")
	if !strings.Contains(with, "<bundle_context>\nsibling info\n</bundle_context>") {
		t.Errorf("sibling context not wrapped:\n%s", with)
	}

	without := buildDistillMessage("P", "", "frag", "body")
	if strings.Contains(without, "<bundle_context>") {
		t.Errorf("bundle_context block should be omitted when no sibling context:\n%s", without)
	}
}
