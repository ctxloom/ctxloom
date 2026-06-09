package sessions

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestNormalizeTimestampNode_UnparseableDegradesToRecent is a regression guard:
// an unparseable timestamp must degrade to ~now, not the zero time. The zero time
// (year 1) sorts a session to the bottom of the picker and falls before its
// day-horizon cutoff, making the session silently invisible — the opposite of the
// fault tolerance the normalization is meant to provide.
func TestNormalizeTimestampNode_UnparseableDegradesToRecent(t *testing.T) {
	n := &yaml.Node{Kind: yaml.ScalarNode, Value: "not-a-timestamp"}

	if changed := normalizeTimestampNode(n); !changed {
		t.Fatal("expected the node to be normalized")
	}

	var got time.Time
	if err := n.Decode(&got); err != nil {
		t.Fatalf("normalized node must re-decode to a time: %v", err)
	}
	if got.IsZero() {
		t.Fatal("unparseable timestamp must NOT degrade to the zero time (hides the session)")
	}
	if d := time.Since(got); d < 0 || d > time.Minute {
		t.Fatalf("expected ~now, got %v (%v ago)", got, d)
	}
}
