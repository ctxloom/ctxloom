package tasks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestHeadline pins Headline's truncation contract, the single source both
// the CLI text list view (root.go's renderTaskTable) and the `compact`
// projection (Task.Compact) now share: first line only, rune-capped at
// HeadlineWidth with a trailing ellipsis when truncated, and a short
// single-line string passed straight through unchanged.
func TestHeadline(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"short single line passes through unchanged", "fix the bug", "fix the bug"},
		{
			"multi-line text keeps only the first line",
			"fix the bug\nprovenance: found 2026-07-24 in session swift-amber",
			"fix the bug",
		},
		{
			"exactly HeadlineWidth runes passes through with no ellipsis",
			strings.Repeat("b", HeadlineWidth),
			strings.Repeat("b", HeadlineWidth),
		},
		{
			"over HeadlineWidth runes is capped with a trailing ellipsis",
			strings.Repeat("a", HeadlineWidth+20),
			strings.Repeat("a", HeadlineWidth) + "…",
		},
		{"empty text stays empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Headline(c.in)
			if got != c.want {
				t.Fatalf("Headline(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestTaskCompact pins Task.Compact's projection: harp id, status, checked,
// tags, and a Headline of the text — and, just as importantly, that it
// carries NONE of Text, Trigger, TextHash, OriginSession, or CreatedAt. This
// is the presentation projection `compact: true` rides on task_list (MCP) and
// `taskloom list --compact` (CLI); see internal/shared/tasks/operations for
// where `limit` (a different, additive knob) is applied instead.
func TestTaskCompact(t *testing.T) {
	full := Task{
		HarpID:        "swift-amber-falcon",
		Text:          "ship the compact projection\nfound while doing the taskloom-compact work",
		Status:        StatusInProgress,
		Checked:       false,
		TextHash:      "deadbeefcafe",
		Trigger:       "",
		OriginSession: "regal-rash-dash",
		Tags:          []string{"release", "urgent"},
		CreatedAt:     time.Now(),
	}

	c := full.Compact()

	if c.HarpID != full.HarpID {
		t.Errorf("HarpID = %q, want %q", c.HarpID, full.HarpID)
	}
	if c.Status != full.Status {
		t.Errorf("Status = %q, want %q", c.Status, full.Status)
	}
	if c.Checked != full.Checked {
		t.Errorf("Checked = %v, want %v", c.Checked, full.Checked)
	}
	if len(c.Tags) != len(full.Tags) {
		t.Fatalf("Tags = %v, want %v", c.Tags, full.Tags)
	}
	for i := range full.Tags {
		if c.Tags[i] != full.Tags[i] {
			t.Errorf("Tags[%d] = %q, want %q", i, c.Tags[i], full.Tags[i])
		}
	}
	wantHeadline := Headline(full.Text)
	if c.Headline != wantHeadline {
		t.Errorf("Headline = %q, want %q", c.Headline, wantHeadline)
	}

	// The JSON shape is a cross-surface contract (like Task's own tags) — no
	// full text/trigger/hash/origin/created_at ever leaks through it.
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, forbidden := range []string{`"text"`, `"trigger"`, `"text_hash"`, `"origin_session"`, `"created_at"`} {
		if strings.Contains(s, forbidden) {
			t.Errorf("CompactTask JSON %s must not contain %s, got %s", s, forbidden, s)
		}
	}
	for _, required := range []string{`"harp_id"`, `"status"`, `"checked"`, `"tags"`, `"headline"`} {
		if !strings.Contains(s, required) {
			t.Errorf("CompactTask JSON must contain %s, got %s", required, s)
		}
	}
}

// An untagged task's compact form still carries "tags", as an EMPTY LIST.
// Output is well-formed and complete: every key of the contract is present on
// every row, whatever the values happen to be. A key that appears only
// sometimes makes a consumer's field access conditional on data it cannot see
// in advance, and `null` is no better than absence — `.tags | join(",")`
// fails on both and succeeds on [].
func TestTaskCompact_EmitsEmptyTagsAsAList(t *testing.T) {
	full, err := newLog(t, "swift-amber-falcon").AddWithTrigger("a task", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	b, err := json.Marshal(full.Compact())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Tags *[]string `json:"tags"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Tags == nil {
		t.Fatalf("compact JSON must carry a tags key, present and non-null: %s", b)
	}
	if len(*got.Tags) != 0 {
		t.Errorf("an untagged task must compact to an empty tag list, got %v", *got.Tags)
	}
}
