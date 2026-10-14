// No build tag, deliberately: the timeout cap decides how long a hung mutant
// holds a worker in every mutation run, and a guard that only runs during the
// hours-long run it guards is not a guard.
package mutation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTestBinary stands in for the test binary `go test` hands its -exec
// program: it prints the flags it was run with, one per line.
func fakeTestBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pkg.test")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write fake test binary: %v", err)
	}
	return bin
}

// gremlinsMutantFlags are the test-binary flags `go test -timeout T -failfast`
// produces for a gremlins mutant run (observed with ps on a live run).
func gremlinsMutantFlags(timeout string) []string {
	return []string{"-test.paniconexit0", "-test.timeout=" + timeout, "-test.failfast=true"}
}

func runExecWrapper(t *testing.T, env []string, args ...string) []string {
	t.Helper()
	wrapper := repoInput(t, "tests/mutation/gremlins_exec.sh")[0]
	cmd := exec.Command(wrapper, append([]string{fakeTestBinary(t)}, args...)...)
	// This suite itself runs under mutation_tmp.sh whenever gremlins' coverage
	// pass covers the module, so the cap's constants are in the inherited
	// environment: each case sets exactly its own.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CTXLOOM_MUTATION_TIMEOUT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gremlins_exec.sh: %v\n%s", err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// The rule is min(coef*B, max(CAP, FLOOR*B)), B = (T-2s)/coef recovered from
// the -timeout gremlins passes (it adds 2s to its own budget). Every row is
// the exact -test.timeout the test binary must receive.
func TestGremlinsExec_CapsTheMutantTestTimeout(t *testing.T) {
	env := []string{
		"CTXLOOM_MUTATION_TIMEOUT_COEF=30",
		"CTXLOOM_MUTATION_TIMEOUT_CAP=600",
		"CTXLOOM_MUTATION_TIMEOUT_FLOOR=5",
	}
	for _, tc := range []struct {
		name, gremlins, want string
	}{
		// B = 60s, package-scoped: 30x is 30m; 5x is 300s, under the cap.
		{"a slow baseline is clamped to the cap", "30m2s", "600s"},
		// The live diff run: B = 140.72s, 30x = 1h10m23s. 5B = 703.6s is
		// above the cap, so the floor stands, rounded up.
		{"the cap never drops below FLOOR x baseline", "1h10m23.70996383s", "704s"},
		// B = 200s: 5B = 1000s.
		{"a slower baseline raises the floor with it", "1h40m2s", "1000s"},
		// triggers: B = 0.021s, 30x = 0.63s. Far under the cap: untouched.
		{"a fast baseline keeps coef x baseline", "2.63s", "2.63s"},
		// B = 20s is where 30x meets the cap: 602s becomes 600s.
		{"the cap binds from CAP/coef up", "10m2s", "600s"},
		// B = 19s: 30x = 570s, under the cap.
		{"just under the cap is untouched", "9m32s", "9m32s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runExecWrapper(t, env, gremlinsMutantFlags(tc.gremlins)...)
			want := gremlinsMutantFlags(tc.want)
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("gremlins passed -timeout %s:\n got %q\nwant %q", tc.gremlins, got, want)
			}
		})
	}
}

// Only a gremlins MUTANT run is capped. Its coverage pass is the baseline
// every budget is derived from, and anything else that runs under the mutation
// tmp (the ooze lanes, the driver's own `go test`) is none of the cap's
// business; without the cap's constants the wrapper is inert.
func TestGremlinsExec_LeavesEveryOtherRunAlone(t *testing.T) {
	env := []string{
		"CTXLOOM_MUTATION_TIMEOUT_COEF=30",
		"CTXLOOM_MUTATION_TIMEOUT_CAP=600",
		"CTXLOOM_MUTATION_TIMEOUT_FLOOR=5",
	}
	coverage := []string{"-test.paniconexit0", "-test.timeout=10m0s", "-test.coverprofile=/tmp/x/coverage", "-test.failfast=true"}
	plain := []string{"-test.paniconexit0", "-test.timeout=4h0m0s", "-test.count=1"}
	for name, args := range map[string][]string{"coverage pass": coverage, "not gremlins": plain} {
		if got := runExecWrapper(t, env, args...); strings.Join(got, " ") != strings.Join(args, " ") {
			t.Errorf("%s: flags changed:\n got %q\nwant %q", name, got, args)
		}
	}
	mutant := gremlinsMutantFlags("1h10m23.70996383s")
	if got := runExecWrapper(t, nil, mutant...); strings.Join(got, " ") != strings.Join(mutant, " ") {
		t.Errorf("unconfigured: flags changed:\n got %q\nwant %q", got, mutant)
	}
}

// End to end, as every recipe runs it: mutation_tmp.sh reads the constants out
// of .gremlins.yaml and installs the wrapper as `go test -exec`, so a mutant
// run under it gets the capped timeout. The live run that motivated the cap
// handed termui mutants 1h10m23.70996383s.
func TestMutationTmp_InstallsTheTimeoutCapFromGremlinsYaml(t *testing.T) {
	in := repoInput(t, "tests/mutation/mutation_tmp.sh", "tests/mutation/gremlins_exec.sh", ".gremlins.yaml")
	script, wrapper := in[0], in[1]
	base := filepath.Join(t.TempDir(), "mutation")

	cmd := exec.Command("bash", script, base, "bash", "-c", `
		for f in $GOFLAGS; do case "$f" in -exec=*) exec "${f#-exec=}" "$@" ;; esac; done
		echo "no -exec in GOFLAGS: $GOFLAGS" >&2; exit 1`,
		"_", fakeTestBinary(t))
	cmd.Args = append(cmd.Args, gremlinsMutantFlags("1h10m23.70996383s")...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mutation_tmp.sh: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	want := gremlinsMutantFlags("704s")
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("a mutant under mutation_tmp.sh:\n got %q\nwant %q", got, want)
	}

	cmd = exec.Command("bash", script, base, "bash", "-c", `printf '%s\n' "$GOFLAGS"`)
	cmd.Env = append(os.Environ(), "GOFLAGS=-trimpath")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mutation_tmp.sh: %v\n%s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), "-trimpath -exec="+wrapper; got != want {
		t.Errorf("GOFLAGS = %q, want the caller's flags plus the wrapper: %q", got, want)
	}
}
