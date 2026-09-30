package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"text/tabwriter"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// validateRootEnv names the directory TestValidateLocalClaudeTranscripts
// reads. Deliberately outside the CTXLOOM_* namespace: the test sandbox
// clears that namespace, and HOME is rewritten under it too, so the recipe
// (`just validate-vendor-claude`) resolves the path before the binary starts.
const validateRootEnv = "VALIDATE_VENDOR_CLAUDE_ROOT"

// noVersion buckets files in which no line carries a `version`.
const noVersion = "(none)"

// TestValidateLocalClaudeTranscripts is the drift pipeline's validate phase,
// run locally: the production read path (OpenAndReadJSONLLines then
// convertLines — exactly what Adapter.Convert does, keeping the accounting
// Convert discards) over every *.jsonl under validateRootEnv, reporting
// AGGREGATES ONLY. Nothing a transcript says is printed; vendor type names
// are the only strings that reach the output.
//
// Each file is grouped by the highest `version` any of its lines records —
// per-file, not per-line, because the reader's accounting is per-file and
// most administrative lines carry no version at all. It fails when a file at
// or above the pinned CLAUDE_CODE_CLI_VERSION has a parse error, an unknown
// line type, or does not convert: that is the evidence a pin bump rests on.
//
// Skipped unless validateRootEnv is set, so ordinary test runs never read a
// developer's transcripts.
func TestValidateLocalClaudeTranscripts(t *testing.T) {
	root := os.Getenv(validateRootEnv)
	if root == "" {
		t.Skipf("%s unset: run `just validate-vendor-claude`", validateRootEnv)
	}
	pin := enginePin(t, "CLAUDE_CODE_CLI_VERSION")
	files := jsonlFiles(t, root)

	// The per-file drop warning is tallied here instead; a line per file on
	// stderr would bury the table.
	defer clidiag.SetSink(io.Discard)()

	byVersion := map[string]*versionStats{}
	scratch := filepath.Join(t.TempDir(), "transcript.jsonl")
	for _, src := range files {
		copyFile(t, src, scratch) // never read the original in place
		tallyFile(byVersion, scratch)
	}

	versions := make([]string, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[i], versions[j]) })

	t.Logf("\nroot=%s files=%d pin=%s (versions >= pin are gated)\n\n%s", root, len(files), pin, renderStats(t, versions, byVersion))

	gated := vendorreader.VersionRange{MinInclusive: pin}
	var failures []string
	for _, v := range versions {
		if in, err := gated.Contains(v); err == nil && in && byVersion[v].failsGate() {
			failures = append(failures, v)
		}
	}
	if len(failures) > 0 {
		t.Fatalf("versions >= pin %s with parse errors or unknown line types: %s", pin, strings.Join(failures, ", "))
	}
}

func jsonlFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	}))
	require.NotEmpty(t, files, "no *.jsonl under %s", root)
	return files
}

// tallyFile reads one (already copied) transcript through the production
// path and adds its accounting to the bucket for its highest version.
func tallyFile(byVersion map[string]*versionStats, path string) {
	lines, err := vendorreader.OpenAndReadJSONLLines("claude", path)
	version := highestVersion(lines)
	st := byVersion[version]
	if st == nil {
		st = newVersionStats()
		byVersion[version] = st
	}
	st.files++
	if err != nil {
		st.readErrors++
		return
	}
	st.records += len(lines)
	rec := &countingRecorder{uses: map[string]bool{}, results: map[string]bool{}}
	acct, err := convertLines(context.Background(), rec, lines)
	if err != nil {
		st.convertErrors++
	}
	st.add(acct, rec)
}

// renderStats is the aggregate table plus, per version, the vendor type
// names behind any unknown-line or dropped-block count.
func renderStats(t *testing.T, versions []string, byVersion map[string]*versionStats) string {
	t.Helper()
	var out strings.Builder
	tw := tabwriter.NewWriter(&out, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "version\tfiles\trecords\tread_err\tconvert_err\tmalformed\tunknown_lines\tdropped_blocks\tunclassified_tool_content\ttool_use\ttool_result\tunanswered_use\torphan_result\tturns\t")
	for _, v := range versions {
		s := byVersion[v]
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t\n", v, s.files, s.records,
			s.readErrors, s.convertErrors, s.malformed, sum(s.unknownLines), sum(s.droppedBlocks),
			s.unclassifiedToolContent, s.toolUses, s.toolResults, s.unansweredUses, s.orphanResults, s.turns)
	}
	require.NoError(t, tw.Flush())
	for _, v := range versions {
		s := byVersion[v]
		if len(s.unknownLines)+len(s.droppedBlocks) > 0 {
			fmt.Fprintf(&out, "\n%s unknown line types: %s; dropped block types: %s", v, labelled(s.unknownLines), labelled(s.droppedBlocks))
		}
	}
	return out.String()
}

type versionStats struct {
	files, records, readErrors, convertErrors, malformed int
	unknownLines, droppedBlocks                          map[string]int
	unclassifiedToolContent, toolUses, toolResults       int
	unansweredUses, orphanResults, turns                 int
}

// failsGate is what disqualifies a version from being pinned: a file that
// could not be read, did not convert, had lines that are not JSON, or had a
// line type this build does not know.
func (s *versionStats) failsGate() bool {
	return s.readErrors+s.convertErrors+s.malformed+sum(s.unknownLines) > 0
}

func newVersionStats() *versionStats {
	return &versionStats{unknownLines: map[string]int{}, droppedBlocks: map[string]int{}}
}

func (s *versionStats) add(a importAccounting, r *countingRecorder) {
	s.malformed += a.malformed
	for k, n := range a.unknownLines.counts {
		s.unknownLines[k] += n
	}
	for k, n := range a.droppedBlocks.counts {
		s.droppedBlocks[k] += n
	}
	s.unclassifiedToolContent += r.unclassified
	s.toolUses += len(r.uses)
	s.toolResults += len(r.results)
	s.turns += r.turns
	for id := range r.uses {
		if !r.results[id] {
			s.unansweredUses++
		}
	}
	for id := range r.results {
		if !r.uses[id] {
			s.orphanResults++
		}
	}
}

// countingRecorder is a transcript.Recorder that keeps counts and call ids,
// never content.
type countingRecorder struct {
	uses, results map[string]bool
	unclassified  int
	turns         int
}

func (r *countingRecorder) Record(ev agent.ChatEvent) error {
	if ev.Complete != nil {
		r.turns++
	}
	if ev.Entry == nil {
		return nil
	}
	switch ev.Entry.Type {
	case agent.EntryTypeToolUse:
		r.uses[ev.Entry.ToolCallID] = true
	case agent.EntryTypeToolResult:
		r.results[ev.Entry.ToolCallID] = true
		for _, b := range ev.Entry.ToolContent {
			// The generic kind with no text: bytes kept, shape unrecognized.
			if b.Kind == agent.KindContent && b.Text == "" {
				r.unclassified++
			}
		}
	}
	return nil
}

func (r *countingRecorder) Close() error { return nil }

// highestVersion is the greatest semver `version` any line records, or
// noVersion. Unparseable lines and values are skipped: parse failures are
// the reader's to count, not this grouping's.
func highestVersion(lines [][]byte) string {
	var best *semver.Version
	for _, l := range lines {
		var v struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(l, &v) != nil || v.Version == "" {
			continue
		}
		sv, err := semver.NewVersion(v.Version)
		if err == nil && (best == nil || sv.GreaterThan(best)) {
			best = sv
		}
	}
	if best == nil {
		return noVersion
	}
	return best.Original()
}

func versionLess(a, b string) bool {
	va, errA := semver.NewVersion(a)
	vb, errB := semver.NewVersion(b)
	if errA != nil || errB != nil {
		return errA != nil && errB == nil // unversioned first
	}
	return va.LessThan(vb)
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func labelled(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	d := dropTally{counts: m}
	return strings.Join(d.labels(""), ", ")
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	require.NoError(t, err)
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	require.NoError(t, err)
	_, err = io.Copy(out, in)
	require.NoError(t, err)
	require.NoError(t, out.Close())
}

// enginePin reads one KEY=value from .github/engine-versions.env.
func enginePin(t *testing.T, key string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "engine-versions.env"))
	require.NoError(t, err)
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == key {
			return v
		}
	}
	t.Fatalf("%s is not pinned in .github/engine-versions.env", key)
	return ""
}
