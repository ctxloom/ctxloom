package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeLauncher is a stand-in for gomutants that writes, to --output, one
// mutant of status per package pattern it was handed — what a real run over
// those packages reports, in shape.
const fakeLauncher = `#!/bin/sh
out=""; prev=""; muts=""; n=0
for a in "$@"; do
  [ "$prev" = --output ] && out=$a
  case "$a" in
    .) f=f.go ;;
    ./*) f="${a#./}/f.go" ;;
    *) prev=$a; continue ;;
  esac
  n=$((n+1))
  muts="$muts${muts:+,}{\"id\":\"$f:F:T#1\",\"status\":\"$STATUS\",\"line\":1}"
  prev=$a
done
printf '%s\n' "$@" > "$ARGV"
killed=0; lived=0
[ "$STATUS" = KILLED ] && killed=$n
[ "$STATUS" = LIVED ] && lived=$n
printf '{"go_module":"lab","files":[{"mutations":[%s]}],"mutants_total":%d,"mutants_killed":%d,"mutants_lived":%d}' "$muts" "$n" "$killed" "$lived" > "$out"
`

// cmdModule is testModule committed on main, as the working directory.
func cmdModule(t *testing.T) (root, launcher, argv string) {
	t.Helper()
	root, _ = testModule(t)
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	launcher = filepath.Join(t.TempDir(), "gomutants")
	if err := os.WriteFile(launcher, []byte(fakeLauncher), 0o755); err != nil {
		t.Fatal(err)
	}
	argv = filepath.Join(t.TempDir(), "argv")
	t.Setenv("ARGV", argv)
	t.Chdir(root)
	return root, launcher, argv
}

// mutshard runs the command line and returns its exit code and output.
func mutshard(args ...string) (int, string) {
	var out bytes.Buffer
	code := run(args, &out, &out)
	return code, out.String()
}

// End to end through the command line: the plan names the shard's packages;
// each shard hands the launcher only its own packages and writes its report;
// the aggregate judges the union against the thresholds it is given, with
// gomutants' own exit codes.
func TestCommands_PlanRunAggregate(t *testing.T) {
	_, launcher, argv := cmdModule(t)

	code, out := mutshard("plan", "-scope", "tree", "-shard", "1", "-shards", "2")
	plan1 := strings.Fields(out)
	if code != 0 || len(plan1) == 0 {
		t.Fatalf("plan: exit %d, output %q", code, out)
	}

	for _, c := range []struct {
		status string
		code   int
		says   string
	}{
		{"KILLED", 0, "Test efficacy: 100.00%"},
		{"LIVED", 10, "test efficacy is not above the threshold"},
	} {
		t.Setenv("STATUS", c.status)
		reports := t.TempDir()
		for _, k := range []string{"0", "1"} {
			if code, out := mutshard("run", "-scope", "tree", "-shard", k, "-shards", "2", "-out", reports, "--", launcher); code != 0 {
				t.Fatalf("run shard %s: exit %d\n%s", k, code, out)
			}
		}
		handed, _ := os.ReadFile(argv)
		var got []string
		for _, a := range strings.Fields(string(handed)) {
			if a == "." || strings.HasPrefix(a, "./") {
				got = append(got, strings.TrimPrefix(a, "./"))
			}
		}
		sort.Strings(got)
		if strings.Join(got, " ") != strings.Join(plan1, " ") {
			t.Errorf("shard 1 handed gomutants %q, its plan is %q", got, plan1)
		}
		var rep report
		b, err := os.ReadFile(filepath.Join(reports, "shard-1.json"))
		if err != nil || json.Unmarshal(b, &rep) != nil || rep.Shard != 1 || rep.Shards != 2 || len(rep.Gomutants) == 0 {
			t.Fatalf("shard-1.json = %s, %v", b, err)
		}
		code, out := mutshard("aggregate", "-scope", "tree", "-reports", reports, "-efficacy", "80")
		if code != c.code || !strings.Contains(out, c.says) {
			t.Errorf("%s: aggregate exit %d (want %d), output must say %q:\n%s", c.status, code, c.code, c.says, out)
		}
	}
}

// A diff run hands gomutants the base; a ratcheted aggregate judges the
// survivors against the package's row, and the threshold miss is not the
// verdict.
func TestCommands_DiffRunAndRatchet(t *testing.T) {
	root, launcher, argv := cmdModule(t)
	writeFile(t, root, "a/a.go", "package a\n\nvar x = 2\n")
	t.Setenv("STATUS", "LIVED")
	reports := t.TempDir()
	if code, out := mutshard("run", "-scope", "diff:main", "-out", reports, "--", launcher); code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out)
	}
	if handed, _ := os.ReadFile(argv); !strings.Contains(string(handed), "--changed-since\nmain\n") || !strings.Contains(string(handed), "./a\n") {
		t.Errorf("gomutants argv = %q, want --changed-since main and ./a", handed)
	}

	reports = t.TempDir()
	if code, out := mutshard("run", "-scope", "tree", "-pkg", "a", "-out", reports, "--", launcher); code != 0 {
		t.Fatalf("run -pkg a: exit %d\n%s", code, out)
	}
	baseline := filepath.Join(t.TempDir(), "b.txt")
	if err := os.WriteFile(baseline, []byte("a 1 1 measured\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := mutshard("aggregate", "-scope", "tree", "-pkg", "a", "-reports", reports, "-efficacy", "80", "-ratchet", baseline)
	if code != 0 || !strings.Contains(out, "not the verdict") || !strings.Contains(out, "HELD") {
		t.Errorf("ratchet over a held row: exit %d\n%s", code, out)
	}
	if err := os.WriteFile(baseline, []byte("a 1 0 measured\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := mutshard("aggregate", "-scope", "tree", "-pkg", "a", "-reports", reports, "-ratchet", baseline); code != 1 || !strings.Contains(out, "REGRESS") && !strings.Contains(out, "more mutants unverified") {
		t.Errorf("ratchet over a lower row: exit %d, want 1\n%s", code, out)
	}
}

// Nothing to measure is a skip — the run writes a report saying so and never
// launches gomutants, and the aggregate passes without reading any report.
func TestCommands_NothingToMeasureSkips(t *testing.T) {
	_, launcher, argv := cmdModule(t)
	reports := t.TempDir()
	code, out := mutshard("run", "-scope", "diff:main", "-out", reports, "--", launcher)
	if code != 0 || !strings.Contains(out, "no mutable Go") {
		t.Fatalf("run over an empty diff: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(argv); err == nil {
		t.Errorf("gomutants was launched over an empty diff")
	}
	if code, out := mutshard("aggregate", "-scope", "diff:main", "-reports", t.TempDir(), "-efficacy", "80"); code != 0 || !strings.Contains(out, "no mutable Go") {
		t.Errorf("aggregate over an empty diff: exit %d\n%s", code, out)
	}
}

// Every way the command line can be wrong is refused, with usage's 2 for a
// missing or unknown command and 1 for a run that cannot proceed.
func TestCommands_Refusals(t *testing.T) {
	cmdModule(t)
	t.Setenv("STATUS", "KILLED")
	for _, c := range []struct {
		args []string
		code int
		says string
	}{
		{nil, 2, "usage"},
		{[]string{"judge"}, 2, "unknown command"},
		{[]string{"run", "-scope", "tree"}, 1, "-out and a launcher"},
		{[]string{"run", "-scope", "tree", "-out", t.TempDir()}, 1, "-out and a launcher"},
		{[]string{"plan", "-scope", "tree", "-shard", "2", "-shards", "2"}, 1, "shard count"},
		{[]string{"aggregate", "-scope", "tree", "-reports", t.TempDir()}, 1, "no shard-*.json"},
		{[]string{"run", "-scope", "tree", "-out", t.TempDir(), "--", "false"}, 1, "gomutants"},
		{[]string{"run", "-scope", "tree", "-out", t.TempDir(), "--", "true"}, 1, "wrote no report"},
	} {
		if code, out := mutshard(c.args...); code != c.code || !strings.Contains(out, c.says) {
			t.Errorf("%q: exit %d (want %d), output must say %q:\n%s", c.args, code, c.code, c.says, out)
		}
	}
	reports := t.TempDir()
	if err := os.WriteFile(filepath.Join(reports, "shard-0.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := mutshard("aggregate", "-scope", "tree", "-reports", reports); code != 1 || !strings.Contains(out, "cannot be read") {
		t.Errorf("an unreadable shard report: exit %d\n%s", code, out)
	}
}
