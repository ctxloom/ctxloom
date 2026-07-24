package tasks

import "strings"

// HeadlineWidth caps the task text a Headline (and, via it, the CLI's
// default human `list` view and every `compact` projection) shows, so
// entries stay scannable instead of running together into a wall. This is
// the number the authoring guidance (task_add's jsonschema description,
// mcpServerInstructions, `taskloom add --help`, loadout.yaml) promises
// authors when it says "keep the subject line to ~80 characters" — change
// one, change the other.
const HeadlineWidth = 80

// Headline collapses text to its first line, capped to HeadlineWidth runes
// with a trailing ellipsis when truncated. Multi-line or long task text
// becomes a single scannable line; the machine-readable full-record views
// (`taskloom show`, task_list without compact) keep the full text.
//
// This is the ONE truncation both the CLI's text `list` view
// (cmd/taskloom/root.go's renderTaskTable) and the `compact` projection
// (Task.Compact) share — hoisted here so the two surfaces can never drift
// on what "the headline" means.
func Headline(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimRight(text, " ")
	r := []rune(text)
	if len(r) <= HeadlineWidth {
		return text
	}
	return strings.TrimRight(string(r[:HeadlineWidth]), " ") + "…"
}

// CompactTask is the presentation projection of a Task for surfaces
// enumerating many tasks at once: `task_list`'s compact=true (MCP) and
// `taskloom list --compact` (CLI). It carries just enough to identify and
// triage a task — harp id, status, checked, tags, and a truncated headline —
// and deliberately OMITS Text, Trigger, TextHash, OriginSession, and
// CreatedAt, which is what makes it cheap to return in bulk: an unfiltered
// listing of a full Task's body for every task is exactly the multi-KB-per-row
// blowup `compact` exists to avoid.
//
// The JSON tags are a cross-surface contract, like Task's own — the MCP tool
// and the CLI's `--format json/yaml/toml/markdown` emit the same snake_case
// keys, so a scripted consumer treats both identically.
//
// `compact` is a PRESENTATION projection applied per-surface at assembly,
// never a query-layer concept: internal/shared/tasks/operations always
// returns full Task values; each frontend (cmd/taskloom's CLI and MCP
// handlers) projects to CompactTask only when the caller asked for it.
type CompactTask struct {
	HarpID   string   `json:"harp_id"`
	Status   string   `json:"status"`
	Checked  bool     `json:"checked"`
	Tags     []string `json:"tags,omitempty"`
	Headline string   `json:"headline"`
}

// Compact projects t to its CompactTask presentation form — see CompactTask's
// doc for what it keeps and, just as deliberately, what it leaves out.
func (t Task) Compact() CompactTask {
	return CompactTask{
		HarpID:   t.HarpID,
		Status:   t.Status,
		Checked:  t.Checked,
		Tags:     t.Tags,
		Headline: Headline(t.Text),
	}
}
