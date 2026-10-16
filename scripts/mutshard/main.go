// Command mutshard splits one gomutants mutation run into disjoint shards that
// run on separate machines, and judges the union of their results as the
// single run would have been judged.
//
//	mutshard plan      -scope S [-pkg DIR] -shard K -shards N          print shard K's packages
//	mutshard run       -scope S [-pkg DIR] -shard K -shards N -out DIR -- <launcher...>
//	mutshard aggregate -scope S [-pkg DIR] -reports DIR [-efficacy P] [-mcover P]
//	                   [-ratchet FILE [-update]]
//
// S is "tree" (the whole module) or "diff:<base>" (gomutants --changed-since
// <base>); -pkg DIR narrows either to the packages under DIR.
//
// gomutants has no sharding of its own, so a shard is a slice of the
// PACKAGES: each runs gomutants over its own packages only. With gomutants'
// default per-package routing a mutant is judged by its own package's tests
// alone, so the union of the shards scores every mutant as one run over all
// the packages would. What a shard does NOT do is judge: it runs with no
// threshold, and `aggregate` applies the thresholds to the summed counts,
// because an efficacy threshold over a slice is a different gate.
//
// The plan is recomputed from the checkout by every shard and by the
// aggregate; each report is stamped with what it covered, so a shard that ran
// a different plan or commit is refused rather than summed.
//
// <launcher...> is the command that runs gomutants (the recipe prefixes the
// per-run temp dir wrapper); its flags and the package patterns are appended.
// When the stdout it passes through is not a terminal, `run` also hands
// gomutants --verbose (unless the launcher already names a verbosity), since
// off a terminal gomutants otherwise prints nothing until its summary and a
// logged or CI run reads as a hang.
// The project's settings are gomutants' own .gomutants.yml, which it reads
// from the module root; this tool reads only its exclude-files and tags, to
// plan the same files gomutants mutates.
//
// -ratchet makes the aggregate of a whole-package run (-scope tree -pkg DIR)
// judge the survivor count against DIR's row in FILE instead of the
// thresholds; -update records the count as the row (ratchet.go).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const gomutantsConfigFile = ".gomutants.yml"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mutshard plan|run|aggregate [flags]")
		return 2
	}
	var err error
	switch args[0] {
	case "plan":
		err = cmdPlan(args[1:], stdout, stderr)
	case "run":
		err = cmdRun(args[1:], stdout, stderr)
	case "aggregate":
		err = cmdAggregate(args[1:], stdout)
	default:
		_, _ = fmt.Fprintf(stderr, "mutshard: unknown command %q\n", args[0])
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mutshard %s: %v\n", args[0], err)
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return exitCode(err)
	}
	return 0
}

// shardFlags are the flags naming one shard of one scope.
type shardFlags struct {
	scope         scope
	shard, shards int
}

func parseShardFlags(fs *flag.FlagSet, args []string, extra func()) (shardFlags, error) {
	var sf shardFlags
	scopeArg := fs.String("scope", "", `"tree" or "diff:<base>"`)
	pkgArg := fs.String("pkg", "", "narrow the scope to the packages under this directory")
	fs.IntVar(&sf.shard, "shard", 0, "this shard's index, 0-based")
	fs.IntVar(&sf.shards, "shards", 1, "the shard count")
	if extra != nil {
		extra()
	}
	if err := fs.Parse(args); err != nil {
		return sf, err
	}
	var err error
	if sf.scope, err = parseScopeFlags(*scopeArg, *pkgArg); err != nil {
		return sf, err
	}
	if sf.shard < 0 || sf.shard >= sf.shards {
		return sf, fmt.Errorf("%w: shard %d of %d", errShardCount, sf.shard, sf.shards)
	}
	return sf, nil
}

func loadPlan(sc scope, shards int) (plan, error) {
	cfg, err := loadGomutantsConfig(gomutantsConfigFile)
	if err != nil {
		return plan{}, err
	}
	return makePlan(".", sc, shards, cfg)
}

func cmdPlan(args []string, stdout, stderr io.Writer) error {
	sf, err := parseShardFlags(flag.NewFlagSet("plan", flag.ContinueOnError), args, nil)
	if err != nil {
		return err
	}
	p, err := loadPlan(sf.scope, sf.shards)
	if err != nil {
		return err
	}
	if p.skip != "" {
		_, _ = fmt.Fprintln(stderr, p.skip)
	}
	for _, d := range p.shards[sf.shard] {
		_, _ = fmt.Fprintln(stdout, d)
	}
	return nil
}

// stampFor is the commit identity every report of this run must share.
func stampFor(sc scope, skipped bool) (stamp, error) {
	st := stamp{scope: sc.String()}
	head, err := gitOut("rev-parse", "HEAD")
	if err != nil {
		return st, err
	}
	st.head = head
	if !sc.tree && !skipped {
		if st.mergeBase, err = gitOut("merge-base", sc.base, "HEAD"); err != nil {
			return st, err
		}
	}
	return st, nil
}

func gitOut(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func cmdRun(args []string, stdout, stderr io.Writer) error {
	var outDir string
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	sf, err := parseShardFlags(fs, args, func() {
		fs.StringVar(&outDir, "out", "", "directory to write shard-<K>.json into")
	})
	if err != nil {
		return err
	}
	launcher := fs.Args()
	if outDir == "" || len(launcher) == 0 {
		return fmt.Errorf("-out and a launcher command after -- are required")
	}
	p, err := loadPlan(sf.scope, sf.shards)
	if err != nil {
		return err
	}
	st, err := stampFor(sf.scope, p.skip != "")
	if err != nil {
		return err
	}
	rep := report{Scope: st.scope, Head: st.head, MergeBase: st.mergeBase, Shard: sf.shard, Shards: sf.shards, Packages: nonNil(p.shards[sf.shard])}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	if rep.Gomutants, err = measureShard(p, sf, outDir, launcher, stdout, stderr); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, fmt.Sprintf("shard-%d.json", sf.shard)), b, 0o644)
}

// measureShard runs gomutants over the shard's packages, or says why there is
// nothing to run: no work in scope at all, or none the plan gave this shard.
// Either leaves the report without a gomutants result.
func measureShard(p plan, sf shardFlags, outDir string, launcher []string, stdout, stderr io.Writer) (json.RawMessage, error) {
	switch {
	case p.skip != "":
		_, _ = fmt.Fprintln(stdout, p.skip)
		return nil, nil
	case len(p.shards[sf.shard]) == 0:
		_, _ = fmt.Fprintf(stdout, "shard %d of %d: the plan gives this shard no packages\n", sf.shard, sf.shards)
		return nil, nil
	}
	return runGomutants(p, sf, outDir, launcher, stdout, stderr)
}

// runGomutants writes gomutants' report under outDir, the directory the
// caller named: a tool here is handed its paths, never reads the process's
// temp root (TestArch_EnvLiteralsOnce).
func runGomutants(p plan, sf shardFlags, outDir string, launcher []string, stdout, stderr io.Writer) (json.RawMessage, error) {
	tmp, err := os.MkdirTemp(outDir, ".mutshard-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	outPath := filepath.Join(tmp, "gomutants.json")
	argv := append(append([]string(nil), launcher...), progressArgs(launcher, isTerminal(stdout))...)
	argv = append(argv, "--output", outPath)
	if !sf.scope.tree {
		argv = append(argv, "--changed-since", sf.scope.base)
	}
	pkgs := p.shards[sf.shard]
	argv = append(argv, patterns(pkgs)...)

	_, _ = fmt.Fprintf(stdout, "shard %d of %d: %d package(s)\n", sf.shard, sf.shards, len(pkgs))
	for _, d := range pkgs {
		_, _ = fmt.Fprintf(stdout, "  %s\n", d)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gomutants: %w", err)
	}
	raw, err := os.ReadFile(outPath)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: gomutants exited 0 and wrote no report", errMeasuredNothing)
	}
	return raw, err
}

// progressArgs is the progress flag gomutants is handed. Off a terminal,
// gomutants prints nothing between discovery and the summary unless --verbose,
// so a logged multi-hour run reads as a hang; on a terminal its progress bar is
// left alone. A verbosity the launcher already names is the caller's choice and
// is kept: --verbose beside a --quiet is a gomutants usage error. A `quiet:` in
// .gomutants.yml needs no check here, since gomutants lets quiet win over verbose.
func progressArgs(launcher []string, tty bool) []string {
	if tty {
		return nil
	}
	for _, a := range launcher {
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && (name == "v" || name == "verbose" || name == "q" || name == "quiet") {
			return nil
		}
	}
	return []string{"--verbose"}
}

// isTerminal is gomutants' own test of its stdout (report.NewTerminal): an
// *os.File that is a character device. The child is handed w itself, so this
// is the stream gomutants writes to and the answer it will reach.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// aggregateFlags is everything `aggregate` is told.
type aggregateFlags struct {
	scope   scope
	reports string
	th      thresholds
	ratchet string
	update  bool
}

func cmdAggregate(args []string, stdout io.Writer) error {
	af, err := parseAggregateFlags(args)
	if err != nil {
		return err
	}
	cfg, err := loadGomutantsConfig(gomutantsConfigFile)
	if err != nil {
		return err
	}
	if idle, err := nothingToMeasure(af.scope, cfg, stdout); err != nil || idle {
		return err
	}
	reports, err := readReports(af.reports)
	if err != nil {
		return err
	}
	if len(reports) == 0 {
		return fmt.Errorf("%w: no shard-*.json under %q", errMissingShard, af.reports)
	}
	p, err := makePlan(".", af.scope, reports[0].Shards, cfg)
	if err != nil {
		return err
	}
	st, err := stampFor(af.scope, false)
	if err != nil {
		return err
	}
	t, verdict := judge(p.shards, st, reports, af.th)
	reportVerdict(stdout, len(p.shards), af.scope, t, verdict)
	return ratchetVerdict(stdout, af, t, verdict)
}

// ratchetVerdict is aggregate's exit: the threshold verdict, unless a
// survivor ratchet was asked for and the verdict judged the union — then the
// ratchet decides, and the verdict is only reported.
func ratchetVerdict(stdout io.Writer, af aggregateFlags, t tally, verdict error) error {
	if af.ratchet == "" || !judged(verdict) {
		return verdict
	}
	if verdict != nil {
		_, _ = fmt.Fprintf(stdout, "(%v — reported, not the verdict: the survivor ratchet is)\n", verdict)
	}
	return ratchet(stdout, af.ratchet, af.scope.pkg, t, af.update)
}

// msgNothingScored is what a pass over a union with no killed and no lived
// mutant says instead of a 0.00% efficacy beside a green verdict.
const msgNothingScored = "no mutant killed or lived — nothing to judge"

// reportVerdict prints the union's counts when the verdict judged one; a
// refusal to judge prints nothing, its counts describing nothing. A pass with
// nothing scored prints the counts it could not score, and no efficacy.
func reportVerdict(stdout io.Writer, shards int, sc scope, t tally, verdict error) {
	switch {
	case verdict == nil && t.scored() == 0:
		_, _ = fmt.Fprintf(stdout, "\nMutation testing, %d shard(s) of %s: %s (every shard looked)\n", shards, sc, msgNothingScored)
		_, _ = fmt.Fprintf(stdout, "Not covered: %d, Timed out: %d, Not viable: %d, Infra error: %d\n", t.notCovered, t.timedOut, t.notViable, t.infraError)
	case judged(verdict):
		printTally(stdout, shards, sc, t)
	}
}

func parseAggregateFlags(args []string) (aggregateFlags, error) {
	var af aggregateFlags
	fs := flag.NewFlagSet("aggregate", flag.ContinueOnError)
	scopeArg := fs.String("scope", "", `"tree" or "diff:<base>"`)
	pkgArg := fs.String("pkg", "", "narrow the scope to the packages under this directory")
	fs.StringVar(&af.reports, "reports", "", "directory holding the shards' shard-<K>.json")
	fs.Float64Var(&af.th.efficacy, "efficacy", 0, "fail at or below this test efficacy %% (0 disables)")
	fs.Float64Var(&af.th.mutantCoverage, "mcover", 0, "fail at or below this mutant coverage %% (0 disables)")
	fs.StringVar(&af.ratchet, "ratchet", "", "judge the survivor count against this baseline file instead of the thresholds")
	fs.BoolVar(&af.update, "update", false, "with -ratchet: record the count as the package's row")
	if err := fs.Parse(args); err != nil {
		return af, err
	}
	var err error
	if af.scope, err = parseScopeFlags(*scopeArg, *pkgArg); err != nil {
		return af, err
	}
	if af.ratchet != "" && (!af.scope.tree || af.scope.pkg == "") {
		return af, errRatchetScope
	}
	if af.update && af.ratchet == "" {
		return af, fmt.Errorf("-update records a ratchet row and needs -ratchet")
	}
	return af, nil
}

// parseScopeFlags is -scope narrowed by -pkg, against the module in the
// working directory — every command reads the same pair, so a package run's
// shards and its aggregate plan alike.
func parseScopeFlags(scopeArg, pkgArg string) (scope, error) {
	sc, err := parseScope(scopeArg)
	if err != nil {
		return sc, err
	}
	return sc.withPkg(".", pkgArg)
}

// nothingToMeasure reports, and says why, when the scope holds no work — in
// which case there is no union to judge. Whether there is any work does not
// depend on the shard count.
func nothingToMeasure(sc scope, cfg *gomutantsConfig, stdout io.Writer) (bool, error) {
	p, err := makePlan(".", sc, 1, cfg)
	if err != nil {
		return false, err
	}
	if p.skip != "" {
		_, _ = fmt.Fprintln(stdout, p.skip)
		return true, nil
	}
	return false, nil
}

// judged reports whether the verdict is a judgement of a complete union — a
// pass or a threshold failure — rather than a refusal to judge at all, whose
// counts would describe nothing.
func judged(verdict error) bool {
	return verdict == nil || errors.Is(verdict, errEfficacy) || errors.Is(verdict, errMutantCoverage)
}

func printTally(stdout io.Writer, shards int, sc scope, t tally) {
	_, _ = fmt.Fprintf(stdout, "\nMutation testing, %d shard(s) of %s:\n", shards, sc)
	_, _ = fmt.Fprintf(stdout, "Killed: %d, Lived: %d, Not covered: %d\n", t.killed, t.lived, t.notCovered)
	_, _ = fmt.Fprintf(stdout, "Timed out: %d, Not viable: %d, Infra error: %d\n", t.timedOut, t.notViable, t.infraError)
	_, _ = fmt.Fprintf(stdout, "Test efficacy: %.2f%%\nMutator coverage: %.2f%%\n", t.efficacy(), t.mutantCoverage())
}

func readReports(dir string) ([]report, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "shard-*.json"))
	if err != nil {
		return nil, err
	}
	var reports []report
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var r report
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", errUnreadable, p, err)
		}
		reports = append(reports, r)
	}
	return reports, nil
}
