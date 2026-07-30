package tagschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The package's malformed-declaration policy, stated once and maintained by
// a test instead of left for a reader to infer from four call sites. The
// rule now has exactly one shape and one named exception:
//
//   - REPORTED. Every structurally malformed declaration is an error --
//     at parse time (add: unparseable, no namespace, wrong namespace,
//     unknown facet, no value) or at access time (Range, and Enum since
//     U126-F01), never silently dropped.
//   - THE ONE EXCEPTION. A `hide` value that is not "true"/"false" is
//     skipped by HideFacts and configures nothing, deliberately -- see that
//     method's doc and TestHideFacts_UninterpretableValueIsSkipped. Making
//     it loud changes what an existing config.yaml does, so it is a
//     config-surface decision, not a sweep's fix (escalated as U126-F04).
//
// This table is the executable form of that rule: adding a facet whose
// malformed value is silently dropped fails here, which is what stops the
// package drifting back to a per-facet policy.
func TestMalformedDeclarationPolicy(t *testing.T) {
	reportedAtParse := []string{
		`not a tag at all`,
		`bare-key=value`,
		`notmagma.arity:"triage:kind"=scalar`,
		`tagma:"triage:kind"=scalar`,
		`tagma.arty:"triage:kind"=scalar`,
		`tagma.arity:"triage:kind"`,
	}
	for _, decl := range reportedAtParse {
		t.Run("parse/"+decl, func(t *testing.T) {
			_, err := Parse([]string{decl})
			require.Error(t, err, "a malformed declaration must not parse clean")
		})
	}

	reportedAtAccess := map[string]func(*Schema) error{
		`tagma.enum:"triage:kind"=","`:  func(s *Schema) error { _, _, err := s.Enum("triage:kind"); return err },
		`tagma.enum:"triage:kind"=""`:   func(s *Schema) error { _, _, err := s.Enum("triage:kind"); return err },
		`tagma.range:"triage:effort"=x`: func(s *Schema) error { _, _, _, err := s.Range("triage:effort"); return err },
		`tagma.range:"triage:effort"="0,5,9"`: func(s *Schema) error {
			_, _, _, err := s.Range("triage:effort")
			return err
		},
	}
	for decl, accessor := range reportedAtAccess {
		t.Run("access/"+decl, func(t *testing.T) {
			s, err := Parse([]string{decl})
			require.NoError(t, err, "this shape is reported at access time, not parse time")
			require.Error(t, accessor(s), "a malformed declaration must be reported somewhere")
		})
	}

	// The one exception, pinned so it stays a single named case rather than
	// a habit: hide is the ONLY facet whose malformed value is dropped.
	t.Run("exception/hide", func(t *testing.T) {
		s, err := Parse([]string{`tagma.hide:"triage:cwe"="yes"`})
		require.NoError(t, err)
		assert.Empty(t, s.HideFacts(), "documented, deliberate, and the only one — see U126-F04")
	})
}

// Well-formed hide declarations still decode both ways.
func TestHideFacts_WellFormedDeclarationsStillDecode(t *testing.T) {
	s, err := Parse([]string{
		`tagma.hide:"triage:cwe"=true`,
		`tagma.hide:"triage:kind"=false`,
	})
	require.NoError(t, err)

	byTarget := map[string]bool{}
	for _, f := range s.HideFacts() {
		byTarget[f.Target] = f.Hide
	}
	assert.Equal(t, map[string]bool{"triage:cwe": true, "triage:kind": false}, byTarget)
}
