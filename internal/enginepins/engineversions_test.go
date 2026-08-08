// Package enginepins is the drift gate for the LLM-engine CLI
// tested-version lock, .github/engine-versions.env. It is the sibling of
// internal/buildpins (which drift-gates .devcontainer/tool-versions.env
// against the dev-image build) and follows the exact same discipline for a
// different file: load the REAL repository files (not fixtures) and assert
// they agree with each other, so a value can't silently go stale or a
// consumer can't silently stop deriving from the lock file.
//
// engine-versions.env pins the last-known-good CLI version per engine that
// ctxloom's reader (internal/transcript/vendorreader/{codex,claude,
// antigravity,kiro}) has been validated against. It is deliberately NOT
// folded into .devcontainer/tool-versions.env / buildpins: that file pins
// build/codegen tooling baked into the devcontainer image, and engine CLIs
// are neither installed there nor part of that build contract (self-healing
// engine-format pipeline plan, Decision A). Its only consumer today is
// .github/workflows/engine-drift-detect.yml (P0: detect + alert only).
package enginepins

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	engineVersionsPath = "../../.github/engine-versions.env"
	workflowPath       = "../../.github/workflows/engine-drift-detect.yml"
	detectScriptPath   = "../../.github/scripts/detect-engine-version.sh"
)

var semverRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)

// parseEngineVersionsEnv loads .github/engine-versions.env's KEY=value
// pairs, skipping comment (#) and blank lines -- same convention as
// .devcontainer/tool-versions.env and the same parsing internal/buildpins
// uses for it.
func parseEngineVersionsEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("%s: line %q is not KEY=value", path, line)
		}
		out[k] = v
	}
	return out
}

// TestEngineVersionsEnvIsWellFormed sanity-checks the lock file itself:
// every value looks like a semver, and the four engines the self-healing
// pipeline plan names (codex, claude-code, antigravity, kiro) are all
// present. A missing or malformed entry here would make every other test in
// this package vacuously pass.
func TestEngineVersionsEnvIsWellFormed(t *testing.T) {
	versions := parseEngineVersionsEnv(t, engineVersionsPath)

	wantKeys := []string{
		"CODEX_CLI_VERSION",
		"CLAUDE_CODE_CLI_VERSION",
		"ANTIGRAVITY_CLI_VERSION",
		"KIRO_CLI_VERSION",
	}
	for _, k := range wantKeys {
		v, ok := versions[k]
		if !ok {
			t.Errorf("%s: missing %s", engineVersionsPath, k)
			continue
		}
		if !semverRE.MatchString(v) {
			t.Errorf("%s: %s=%q does not look like a semver (want digits and dots)", engineVersionsPath, k, v)
		}
	}
	if len(versions) != len(wantKeys) {
		t.Errorf("%s: expected exactly %d entries (%v), found %d (%v) -- a new engine key needs a matching matrix entry in %s, and vice versa",
			engineVersionsPath, len(wantKeys), wantKeys, len(versions), versions, workflowPath)
	}
}

// matrixLockKeyRE matches one `- engine: <name>` / `lock_key: <KEY>` pair in
// engine-drift-detect.yml's matrix `include:` list, e.g.:
//
//   - engine: codex
//     lock_key: CODEX_CLI_VERSION
var matrixLockKeyRE = regexp.MustCompile(`(?m)^\s*-\s*engine:\s*(\S+)\s*\n\s*lock_key:\s*(\S+)\s*$`)

// matrixEntries returns engine name -> lock_key for every matrix include
// entry found in the workflow.
func matrixEntries(t *testing.T, content string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range matrixLockKeyRE.FindAllStringSubmatch(content, -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatalf("%s: found no `- engine: ... / lock_key: ...` matrix entries -- regex or workflow structure changed", workflowPath)
	}
	return out
}

// TestWorkflowMatrixMatchesEngineVersionsEnv is the core structural-can't-
// diverge check, the same shape as buildpins'
// TestDockerfileArgsAreDefaultlessAndMatchToolVersionsEnv: the set of
// lock_key values in engine-drift-detect.yml's matrix must be EXACTLY the
// set of keys in engine-versions.env, in both directions. A lock-file key
// with no matrix entry is never checked for drift; a matrix entry with no
// lock-file key can never resolve a "pinned" value (the workflow's own "Read
// tested-version lock" step would fail at runtime with `::error::` -- this
// test catches that at commit time instead of on the next scheduled run).
func TestWorkflowMatrixMatchesEngineVersionsEnv(t *testing.T) {
	versions := parseEngineVersionsEnv(t, engineVersionsPath)

	raw, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}
	matrix := matrixEntries(t, string(raw))

	var matrixKeys []string
	for _, lockKey := range matrix {
		matrixKeys = append(matrixKeys, lockKey)
	}
	sort.Strings(matrixKeys)

	var fileKeys []string
	for k := range versions {
		fileKeys = append(fileKeys, k)
	}
	sort.Strings(fileKeys)

	for _, k := range fileKeys {
		found := false
		for _, mk := range matrixKeys {
			if mk == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s declares %s, but %s's matrix has no `lock_key: %s` -- this engine is never checked for drift", engineVersionsPath, k, workflowPath, k)
		}
	}
	for _, mk := range matrixKeys {
		if _, ok := versions[mk]; !ok {
			t.Errorf("%s's matrix references `lock_key: %s`, but %s has no %s= entry -- the workflow's \"Read tested-version lock\" step would fail at runtime", workflowPath, mk, engineVersionsPath, mk)
		}
	}
}

// engineCaseRE matches one `case`-arm engine name in
// detect-engine-version.sh's `case "$engine" in ... esac`, e.g. `codex)` or
// `claude-code)` at the start of a line.
var engineCaseRE = regexp.MustCompile(`(?m)^([a-z][a-z0-9-]*)\)\s*$`)

// TestDetectScriptHandlesEveryMatrixEngine asserts every `engine:` name used
// in the workflow's matrix has a matching case arm in
// detect-engine-version.sh, and vice versa -- so a new engine added to one
// can't silently be missing from the other (the workflow would call the
// script with an engine it doesn't recognize, hitting the script's `exit 1`
// "unknown engine" branch on every scheduled run).
func TestDetectScriptHandlesEveryMatrixEngine(t *testing.T) {
	workflowRaw, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}
	matrix := matrixEntries(t, string(workflowRaw))
	var matrixEngines []string
	for engine := range matrix {
		matrixEngines = append(matrixEngines, engine)
	}
	sort.Strings(matrixEngines)

	scriptRaw, err := os.ReadFile(detectScriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", detectScriptPath, err)
	}
	scriptContent := string(scriptRaw)
	caseArms := map[string]bool{}
	for _, m := range engineCaseRE.FindAllStringSubmatch(scriptContent, -1) {
		caseArms[m[1]] = true
	}
	if len(caseArms) == 0 {
		t.Fatalf("%s: found no `<engine>)` case arms -- regex or script structure changed", detectScriptPath)
	}
	// The wildcard `*)` error arm isn't a real engine.
	delete(caseArms, "*")

	for _, engine := range matrixEngines {
		if !caseArms[engine] {
			t.Errorf("%s's matrix has `engine: %s`, but %s has no `%s)` case arm -- every scheduled run for this engine would hit the script's \"unknown engine\" branch", workflowPath, engine, detectScriptPath, engine)
		}
	}
	for engine := range caseArms {
		found := false
		for _, me := range matrixEngines {
			if me == engine {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s has a `%s)` case arm, but %s's matrix has no `engine: %s` -- dead code that's never invoked", detectScriptPath, engine, workflowPath, engine)
		}
	}
}
