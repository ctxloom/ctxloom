//go:build acceptance && integration && coveragegate

package acceptance

import (
	"bufio"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/cli"
)

// The completeness gate, answered from what the CLI's own code DID rather than
// from what the suite says it ran.
//
// It is a separate build tag and a second `go test` invocation because
// coverage data is only complete once every instrumented exec has flushed its
// counters — which is after the suite this would otherwise run inside. See the
// `test-acceptance-cover` recipe, which runs the suite and then this.
//
// WHY THIS IS STRONGER THAN COUNTING ARGV. Recording the command lines a suite
// launches proves an argv was STARTED, not that any of the leaf's code ran: a
// process that dies in flag parsing, argument validation or config load still
// credits its leaf. Coverage cannot make that mistake — a block's counter is
// incremented by the block executing, and by nothing else.
//
// Its opposite failure — a process killed before it flushes — is real and was
// live in this harness until the MCP client stopped SIGKILLing its server;
// `mcp serve` read 0.0% while running in every @mcp scenario. Any leaf added
// to the allowlist below on a 0% reading must be checked against that shape
// first, because "never ran" and "killed before it could say so" look
// identical here.

// coverProfileEnv names the textfmt-converted coverage profile this gate
// reads. The recipe produces it with `go tool covdata textfmt`.
const coverProfileEnv = "CTXLOOM_COVERPROFILE"

// modulePath prefixes every file name in a textfmt profile.
const modulePath = "github.com/ctxloom/ctxloom"

// coverageExemptLeaves are leaves no hermetic scenario executes, each with the
// reason. Same contract as completeness_test.go's lists: the gate fails on any
// difference in EITHER direction, so an entry that starts being covered has to
// come out.
// `bundle push` and `container build` are NOT here, though the text-keyed
// gate excludes both. Coverage says their RunE runs — they reach the point of
// refusing, which is real execution of real code. An exemption for them would
// be describing an intent, not the program.
// `acp serve` LEFT this list when its --workspace/--agent validation gained a
// scenario (features/cli/acp.feature). The exemption had said it "serves the
// protocol on stdio until the client disconnects", which is no longer the whole
// truth: the suite drives it with stdin at /dev/null, so acpagent.Serve's
// JSON-RPC loop reads immediate EOF and returns cleanly. That is real execution
// of real code — the same standard by which `bundle push` and `container build`
// are absent here — but note what it does NOT prove: no protocol exchange
// happens, so a scenario asserting `acp serve` SUCCEEDS is asserting it got past
// validation and exited, not that it served anything.
var coverageExemptLeaves = map[string]string{
	"ctxloom acp run":                  "connects OUT to a live ACP agent; no hermetic peer",
	"ctxloom session transcript watch": "long-lived file watcher; no hermetic exit",
	"ctxloom plan watch":               "long-lived file watcher; no hermetic exit",
	"ctxloom remote discover":          "network discovery search; no deterministic fixture",

	// HIDDEN machine callbacks. Invisible to the text-keyed census, which
	// skips hidden commands entirely, so this gate is the first thing to
	// report them at all. They are exempt because nothing a user does reaches
	// them directly, not because they do not matter — see the task noted in
	// the plan about giving the two llm ones real coverage.
	"ctxloom llm host":             "go-plugin host callback; reached only by a plugin handshake",
	"ctxloom llm turn":             "go-plugin turn callback; reached only by a plugin handshake",
	"ctxloom container provenance": "hidden diagnostic reading image labels; needs a built image",
}

// coverBlock is one entry of a textfmt coverage profile: the statements
// between two positions in a file, and how many times they ran.
//
// startCol is carried because a LINE does not identify a block. Consecutive
// `if` statements produce two blocks that start on the same line: the one
// reaching the `if` (which runs whenever control gets that far, whatever the
// condition says) and the guarded body itself, starting at the `{`. Keying by
// line alone would let the first vouch for the second — a block that ran
// standing in for one that did not. leafRan does not need the column and
// ignores it; cli_flag_gate_test.go cannot be correct without it.
type coverBlock struct {
	file      string
	startLine int
	startCol  int
	count     int
}

func TestCLICoverage_EveryLeafActuallyRan(t *testing.T) {
	profile := os.Getenv(coverProfileEnv)
	if profile == "" {
		t.Fatalf("%s is unset: this gate reads a textfmt coverage profile and cannot "+
			"infer one. Run it through `just test-acceptance-cover`, which produces the "+
			"profile and then invokes this. Skipping instead of failing would report a "+
			"clean gate over no data at all.", coverProfileEnv)
	}
	blocks, err := readCoverProfile(profile)
	if err != nil {
		t.Fatalf("read coverage profile %s: %v", profile, err)
	}
	// A profile that parsed to nothing is not a pass. It is the shape this
	// project is named for: a clean-looking result over zero bytes.
	if len(blocks) == 0 {
		t.Fatalf("coverage profile %s contains no blocks — the instrumented binary "+
			"produced no data, so this gate measured nothing", profile)
	}

	var uncovered, unexpectedlyCovered []string
	for _, leaf := range coverableLeaves(cli.GetRootCmd()) {
		ran, ok := leafRan(leaf.cmd, blocks)
		if !ok {
			t.Errorf("%s: could not locate its RunE in the coverage profile. A leaf whose "+
				"code cannot be found is not a leaf this gate can vouch for.", leaf.path)
			continue
		}
		_, exempt := coverageExemptLeaves[leaf.path]
		switch {
		case !ran && !exempt:
			uncovered = append(uncovered, leaf.path)
		case ran && exempt:
			unexpectedlyCovered = append(unexpectedlyCovered, leaf.path)
		}
	}
	sort.Strings(uncovered)
	sort.Strings(unexpectedlyCovered)

	for _, p := range uncovered {
		t.Errorf("%s: no statement of its RunE ran in the whole acceptance suite. Give it a "+
			"scenario, or add it to coverageExemptLeaves with the reason.", p)
	}
	// The other direction, so the list cannot rot into a silencer.
	for _, p := range unexpectedlyCovered {
		t.Errorf("%s is in coverageExemptLeaves but its code RAN. Remove the entry: an "+
			"exemption that no longer applies hides the next real gap.", p)
	}
}

// leaf pairs a command with its path, so the gate can report a name while
// reflecting over the command.
type leaf struct {
	path string
	cmd  *cobra.Command
}

// coverableLeaves walks the real command tree for every runnable leaf that has
// a RunE to attribute coverage to. Hidden commands are INCLUDED: they run the
// same code as any other and a census that cannot see them cannot vouch for
// them either.
func coverableLeaves(root *cobra.Command) []leaf {
	var out []leaf
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		children := c.Commands()
		hasVisibleChild := false
		for _, ch := range children {
			if !ch.Hidden && ch.Name() != "help" {
				hasVisibleChild = true
			}
			walk(ch)
		}
		if !hasVisibleChild && c.Runnable() && c != root && c.RunE != nil {
			out = append(out, leaf{path: c.CommandPath(), cmd: c})
		}
	}
	walk(root)
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// leafRan reports whether any statement of cmd's RunE executed, and whether
// the function could be located in the profile at all.
//
// The join is by FILE AND LINE, deliberately, not by symbol name. A RunE
// written as an inline func literal in a package-level var compiles to
// `init.funcN`, and `go tool covdata func` omits closures from its output
// entirely — so a name-keyed lookup loses those leaves silently rather than
// reporting them uncovered. Position is carried for every block regardless of
// how the function was written.
func leafRan(cmd *cobra.Command, blocks map[string][]coverBlock) (ran bool, found bool) {
	pc := reflect.ValueOf(cmd.RunE).Pointer()
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return false, false
	}
	file, line := fn.FileLine(pc)
	for suffix, fileBlocks := range blocks {
		if !strings.HasSuffix(file, suffix) {
			continue
		}
		// FileLine gives the DECLARATION line; the first coverage block starts
		// at the first STATEMENT, a line or more below. So take the nearest
		// block at or after the declaration — the function's entry block —
		// rather than requiring an exact line match, which never holds.
		best := -1
		for _, b := range fileBlocks {
			if b.startLine < line {
				continue
			}
			if best == -1 || b.startLine < fileBlocks[best].startLine {
				best = indexOfBlock(fileBlocks, b)
			}
		}
		if best == -1 {
			return false, false
		}
		return fileBlocks[best].count > 0, true
	}
	return false, false
}

// indexOfBlock returns b's position in blocks, comparing by start line.
func indexOfBlock(blocks []coverBlock, b coverBlock) int {
	for i := range blocks {
		if blocks[i].startLine == b.startLine {
			return i
		}
	}
	return -1
}

// readCoverProfile parses a textfmt coverage profile into blocks keyed by the
// package-relative file path the profile uses (e.g.
// "github.com/ctxloom/ctxloom/internal/cli/remote.go").
func readCoverProfile(path string) (map[string][]coverBlock, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	out := map[string][]coverBlock{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// "mode: set" header, blank lines.
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		// <file>:<startLine>.<col>,<endLine>.<col> <numStmt> <count>
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		colon := strings.LastIndex(fields[0], ":")
		if colon < 0 {
			continue
		}
		// A textfmt profile names files by IMPORT PATH
		// ("github.com/ctxloom/ctxloom/internal/cli/remote.go") while the
		// runtime reports an absolute filesystem path. Strip the module prefix
		// so the two can be matched by suffix.
		file := strings.TrimPrefix(fields[0][:colon], modulePath+"/")
		// "<startLine>.<startCol>,<endLine>.<endCol>" — take the start pair.
		start, _, ok := strings.Cut(fields[0][colon+1:], ",")
		if !ok {
			continue
		}
		lineStr, colStr, ok := strings.Cut(start, ".")
		if !ok {
			continue
		}
		startLine, err := strconv.Atoi(lineStr)
		if err != nil {
			continue
		}
		startCol, err := strconv.Atoi(colStr)
		if err != nil {
			continue
		}
		out[file] = append(out[file], coverBlock{
			file: file, startLine: startLine, startCol: startCol, count: count,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}
