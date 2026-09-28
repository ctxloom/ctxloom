// Command mutshard splits one gremlins mutation run into disjoint shards that
// run on separate machines, and judges the union of their results as the
// single run would have been judged.
//
//	mutshard plan      -scope S -shard K -shards N          print shard K's files
//	mutshard run       -scope S -shard K -shards N -out DIR -- <launcher...>
//	mutshard aggregate -scope S -reports DIR
//
// S is "tree" (the whole module) or "diff:<base>" (gremlins --diff <base>).
//
// A shard is the SAME gremlins invocation as the unsharded run — whole-module
// coverage, the same timeout budget (derived from that coverage run), the
// same per-package test for each mutant — with every other shard's files added
// to the project's exclusions. A mutant's verdict depends only on its own
// package's tests and that budget, so the union of the shards scores every
// mutant as the single run would. What a shard does NOT do is judge: its
// thresholds are zeroed and `aggregate` applies the project's to the summed
// counts, because an efficacy threshold over a slice is a different gate.
//
// The plan is recomputed from the checkout by every shard and by the
// aggregate; each report is stamped with what it covered, so a shard that ran
// a different plan or commit is refused rather than summed.
//
// <launcher...> is the command that runs gremlins (the recipe prefixes the
// per-run temp dir wrapper); `unleash` and its arguments are appended.
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

const gremlinsConfigFile = ".gremlins.yaml"

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
		err = cmdAggregate(args[1:], stdout, stderr)
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
	fs.IntVar(&sf.shard, "shard", 0, "this shard's index, 0-based")
	fs.IntVar(&sf.shards, "shards", 1, "the shard count")
	if extra != nil {
		extra()
	}
	if err := fs.Parse(args); err != nil {
		return sf, err
	}
	var err error
	if sf.scope, err = parseScope(*scopeArg); err != nil {
		return sf, err
	}
	if sf.shard < 0 || sf.shard >= sf.shards {
		return sf, fmt.Errorf("%w: shard %d of %d", errShardCount, sf.shard, sf.shards)
	}
	return sf, nil
}

func loadPlan(sc scope, shards int) (plan, error) {
	cfg, err := loadGremlinsConfig(gremlinsConfigFile)
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
	for _, f := range p.shards[sf.shard] {
		_, _ = fmt.Fprintln(stdout, f)
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
	cfg, err := loadGremlinsConfig(gremlinsConfigFile)
	if err != nil {
		return err
	}
	p, err := makePlan(".", sf.scope, sf.shards, cfg)
	if err != nil {
		return err
	}
	st, err := stampFor(sf.scope, p.skip != "")
	if err != nil {
		return err
	}
	rep := report{Scope: st.scope, Head: st.head, MergeBase: st.mergeBase, Shard: sf.shard, Shards: sf.shards, Files: nonNil(p.shards[sf.shard])}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	switch {
	case p.skip != "":
		_, _ = fmt.Fprintln(stdout, p.skip)
	case len(rep.Files) == 0:
		_, _ = fmt.Fprintf(stdout, "shard %d of %d: the plan gives this shard no files\n", sf.shard, sf.shards)
	default:
		if rep.Gremlins, err = runGremlins(cfg, p, sf, outDir, launcher, stdout, stderr); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, fmt.Sprintf("shard-%d.json", sf.shard)), b, 0o644)
}

// runGremlins stages the shard's derived config under outDir, the directory the
// caller named: a tool here is handed its paths, never reads the process's temp
// root (TestArch_EnvLiteralsOnce).
func runGremlins(cfg *gremlinsConfig, p plan, sf shardFlags, outDir string, launcher []string, stdout, stderr io.Writer) (json.RawMessage, error) {
	var others []string
	for k, files := range p.shards {
		if k != sf.shard {
			others = append(others, files...)
		}
	}
	tmp, err := os.MkdirTemp(outDir, ".mutshard-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	shardCfg, err := cfg.shardConfig(others)
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(tmp, "gremlins.yaml")
	if err := os.WriteFile(cfgPath, shardCfg, 0o644); err != nil {
		return nil, err
	}
	outPath := filepath.Join(tmp, "gremlins.json")
	argv := append(append([]string(nil), launcher...), "unleash")
	if !sf.scope.tree {
		argv = append(argv, "--diff", sf.scope.base)
	}
	argv = append(argv, "--config", cfgPath, "--output", outPath)

	files := p.shards[sf.shard]
	_, _ = fmt.Fprintf(stdout, "shard %d of %d: %d file(s)\n", sf.shard, sf.shards, len(files))
	for _, f := range files {
		_, _ = fmt.Fprintf(stdout, "  %s\n", f)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gremlins: %w", err)
	}
	raw, err := os.ReadFile(outPath)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: gremlins exited 0 and wrote no report (it found no mutant at all)", errMeasuredNothing)
	}
	return raw, err
}

func cmdAggregate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("aggregate", flag.ContinueOnError)
	scopeArg := fs.String("scope", "", `"tree" or "diff:<base>"`)
	dir := fs.String("reports", "", "directory holding the shards' shard-<K>.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sc, err := parseScope(*scopeArg)
	if err != nil {
		return err
	}
	cfg, err := loadGremlinsConfig(gremlinsConfigFile)
	if err != nil {
		return err
	}
	// Whether there is any work does not depend on the shard count.
	if p, err := makePlan(".", sc, 1, cfg); err != nil {
		return err
	} else if p.skip != "" {
		_, _ = fmt.Fprintln(stdout, p.skip)
		return nil
	}
	reports, err := readReports(*dir)
	if err != nil {
		return err
	}
	if len(reports) == 0 {
		return fmt.Errorf("%w: no shard-*.json under %q", errMissingShard, *dir)
	}
	p, err := makePlan(".", sc, reports[0].Shards, cfg)
	if err != nil {
		return err
	}
	st, err := stampFor(sc, false)
	if err != nil {
		return err
	}
	t, verdict := judge(p.shards, st, reports, thresholds{efficacy: cfg.efficacy, mutantCoverage: cfg.mutantCoverage})
	if verdict == nil || errors.Is(verdict, errEfficacy) || errors.Is(verdict, errMutantCoverage) {
		_, _ = fmt.Fprintf(stdout, "\nMutation testing, %d shard(s) of %s:\n", len(p.shards), sc)
		_, _ = fmt.Fprintf(stdout, "Killed: %d, Lived: %d, Not covered: %d\n", t.killed, t.lived, t.notCovered)
		_, _ = fmt.Fprintf(stdout, "Timed out: %d, Not viable: %d, Skipped: %d\n", t.timedOut, t.notViable, t.skipped)
		_, _ = fmt.Fprintf(stdout, "Test efficacy: %.2f%%\nMutator coverage: %.2f%%\n", t.efficacy(), t.mutantCoverage())
	}
	return verdict
}

func readReports(dir string) ([]report, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "shard-*.json"))
	if err != nil {
		return nil, err
	}
	var reports []report
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var r report
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", errUnreadable, path, err)
		}
		reports = append(reports, r)
	}
	return reports, nil
}
