// Package buildpins is the drift gate for ctxloom's pinned codegen/build
// tool versions. .devcontainer/tool-versions.env is meant to be the ONE
// place these versions are declared; the Dockerfile, the justfile, and
// .github/workflows/{ci,release-completer}.yml are all supposed to derive
// from it instead of hand-copying version numbers.
//
// That "supposed to" is exactly what failed once already: buf.gen.yaml
// switched to pinned `local:` plugins (commit b334605) and
// release-completer.yml — which runs outside the devcontainer image, so it
// can't just inherit the pin — was never updated
// to install them. It only kept working by accident, because it still used
// unpinned/BSR-remote plugins; the next release's codegen would have failed
// outright. fe0e322 fixed that occurrence by hand. This package exists so the
// NEXT occurrence fails a test instead of a release.
//
// Every test here loads the REAL repository files (not fixtures), the same
// pattern cmd/ltk/project_config_test.go uses for its own drift gate: load
// the real config and assert real behavior against it, since nothing else in
// the repo evaluates these files against each other.
package buildpins

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

const (
	toolVersionsPath     = "../../../.devcontainer/tool-versions.env"
	dockerfilePath       = "../../../.devcontainer/Dockerfile"
	justfilePath         = "../../../justfile"
	ciJustfilePath       = "../../../build/ci.justfile"
	setupJustActionPath  = "../../../.github/actions/setup-just/action.yml"
	ciWorkflowPath       = "../../../.github/workflows/ci.yml"
	releaseWorkflowPath  = "../../../.github/workflows/release-completer.yml"
	devcontainerJSONPath = "../../../.devcontainer/devcontainer.json"
)

// releaseCompleterKeys are the tool-versions.env keys that
// release-completer.yml must independently install (it runs in the official
// Go image, not the devcontainer image, so it can't just inherit them from
// there): the codegen tools implicated in the historical failure this package
// guards against, plus goreleaser itself.
var releaseCompleterKeys = []string{
	"GORELEASER_VERSION",
	"BUF_VERSION",
	"PROTOC_GEN_GO_VERSION",
	"PROTOC_GEN_GO_GRPC_VERSION",
	"VERSIONATOR_VERSION",
}

var versionValueRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)

// A checksum pin is `<TOOL>_SHA256_<ARCH>`: the sha256 of the download that
// `<TOOL>_VERSION` names, for one TARGETARCH. scripts/lint-pins keeps it
// moving in step with its version; the tests here keep it well-formed and
// wired into the Dockerfile.
var (
	checksumKeyRE   = regexp.MustCompile(`^(.+)_SHA256_([A-Z0-9]+)$`)
	checksumValueRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// A revision pin is `<TOOL>_REVISION`: the full git commit SHA a tool with no
// published release is built from (REPRISE_REVISION). An abbreviated SHA is
// refused — it names whichever commit matches today, not a fixed one.
var (
	revisionKeyRE   = regexp.MustCompile(`^.+_REVISION$`)
	revisionValueRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// imageArches are the TARGETARCH values the devcontainer image is built for —
// the arms of the Dockerfile's per-arch `case` statements.
var imageArches = []string{"AMD64", "ARM64"}

// runnerOnlyPinConsumers are the files that read a checksum pin for a
// platform the image is not built for. CI runners outside the image (macOS,
// Windows) install `just` themselves, from the same file, so their archives'
// checksums live in tool-versions.env too, keyed by an arch suffix that is not
// an imageArches entry (JUST_SHA256_DARWINARM64). The Dockerfile never sees
// those; the consumer named here must.
var runnerOnlyPinConsumers = []string{setupJustActionPath}

// isRunnerOnlyPin reports whether key is a checksum pin for a platform the
// image is not built for, and so no Dockerfile ARG or devcontainer.json
// build arg carries it.
func isRunnerOnlyPin(key string) bool {
	m := checksumKeyRE.FindStringSubmatch(key)
	return m != nil && !slices.Contains(imageArches, m[2])
}

// imageKeys returns tool-versions.env's keys that reach the image build —
// every key but the runner-only checksum pins — sorted.
func imageKeys(versions map[string]string) []string {
	var keys []string
	for k := range versions {
		if !isRunnerOnlyPin(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// isPinArg reports whether a Dockerfile ARG carries a tool-versions.env pin.
func isPinArg(name string) bool {
	return strings.HasSuffix(name, "_VERSION") || checksumKeyRE.MatchString(name) || revisionKeyRE.MatchString(name)
}

// parseToolVersionsEnv loads .devcontainer/tool-versions.env's KEY=value
// pairs, skipping comment (#) and blank lines exactly as every consumer's
// `grep -v '^#' | grep -v '^$'` does.
func parseToolVersionsEnv(t *testing.T, path string) map[string]string {
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

// readFile is the "this file is part of the build contract" reader: a missing
// file is a fatal test failure, never a skip.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// recipeBody returns the body of a single just recipe.
//
// Every CI step is a `just` target now (see build/ci.justfile), so the
// derivation these tests police — "the pin comes from tool-versions.env, it is
// not hand-copied" — lives in a RECIPE rather than in a workflow's `run:`
// block. Checking the workflow text alone would therefore pass vacuously on
// nothing but a `run: just <target>` line. Each test below instead asserts
// BOTH hops: that the workflow invokes the recipe, and that the recipe derives
// from the file.
//
// A recipe starts at column 0 with its name and runs until the next line that
// is neither blank nor indented.
func recipeBody(t *testing.T, justfilePath, recipe string) string {
	t.Helper()
	content := readFile(t, justfilePath)
	header := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(recipe) + `(\s+[^:\n]*)?:`)
	loc := header.FindStringIndex(content)
	if loc == nil {
		t.Fatalf("%s: no recipe named %q — a workflow step invoking it would fail at runtime with just's \"unknown recipe\"", justfilePath, recipe)
	}
	rest := content[loc[1]:]
	for i, line := range strings.Split(rest, "\n") {
		if i == 0 || line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		return rest[:strings.Index(rest, "\n"+line)]
	}
	return rest
}

// assertInvokes fails unless the workflow text runs `just <recipe>`. This is
// the wiring half of every two-hop assertion below: a recipe that derives its
// pins perfectly but that no workflow calls is a gate that does not gate —
// the failure mode `test-conformance` already exhibits in this repo.
func assertInvokes(t *testing.T, workflowPath, workflowText, recipe string) {
	t.Helper()
	if !regexp.MustCompile(`\bjust\s+` + regexp.QuoteMeta(recipe) + `\b`).MatchString(workflowText) {
		t.Errorf("%s never runs `just %s` — the recipe that derives the tool pins is not wired to any step here", workflowPath, recipe)
	}
}

// dockerfileArg records one `ARG NAME` (optionally `=default`) declaration.
type dockerfileArg struct {
	name       string
	hasDefault bool
}

var dockerfileArgRE = regexp.MustCompile(`(?m)^ARG\s+([A-Za-z0-9_]+)(?:=(.*))?\s*$`)

func parseDockerfileArgs(t *testing.T, path string) map[string]dockerfileArg {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]dockerfileArg{}
	for _, m := range dockerfileArgRE.FindAllStringSubmatch(string(raw), -1) {
		name := m[1]
		out[name] = dockerfileArg{name: name, hasDefault: m[2] != ""}
	}
	return out
}

// TestToolVersionsEnvIsWellFormed sanity-checks the source-of-truth file
// itself: every value looks like a version, and there's more than one entry
// (an empty or single-entry file would make every other test in this package
// vacuously pass).
func TestToolVersionsEnvIsWellFormed(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)
	if len(versions) < 5 {
		t.Fatalf("%s: expected at least 5 pinned tool versions, found %d (%v)", toolVersionsPath, len(versions), versions)
	}
	for k, v := range versions {
		if checksumKeyRE.MatchString(k) {
			if !checksumValueRE.MatchString(v) {
				t.Errorf("%s: %s=%q is not a sha256 (want 64 lowercase hex characters)", toolVersionsPath, k, v)
			}
			continue
		}
		if revisionKeyRE.MatchString(k) {
			if !revisionValueRE.MatchString(v) {
				t.Errorf("%s: %s=%q is not a full git commit SHA (want 40 lowercase hex characters)", toolVersionsPath, k, v)
			}
			continue
		}
		if !versionValueRE.MatchString(v) {
			t.Errorf("%s: %s=%q does not look like a version (want digits and dots)", toolVersionsPath, k, v)
		}
	}
}

// TestDockerfileArgsAreDefaultlessAndMatchToolVersionsEnv is the core Dockerfile
// side of the drift gate. It asserts, both directions:
//   - every ARG in the Dockerfile whose name ends in _VERSION has NO default
//     value (a default would be a second source of truth that could silently
//     disagree with tool-versions.env — see the Dockerfile's own guard RUN);
//   - the set of such ARG names is EXACTLY the set of keys in
//     tool-versions.env (an ARG with no matching file entry could never be
//     supplied; a file entry with no matching ARG is dead weight that looks
//     wired up but isn't).
func TestDockerfileArgsAreDefaultlessAndMatchToolVersionsEnv(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)
	args := parseDockerfileArgs(t, dockerfilePath)

	var argVersionNames []string
	for name, arg := range args {
		if !isPinArg(name) {
			continue
		}
		argVersionNames = append(argVersionNames, name)
		if arg.hasDefault {
			t.Errorf("Dockerfile: ARG %s has a default value — tool pin ARGs must be default-less so %s stays the only source of truth (a default would silently diverge from it)", name, toolVersionsPath)
		}
	}

	fileKeys := imageKeys(versions)
	sort.Strings(argVersionNames)

	for _, k := range fileKeys {
		if _, ok := args[k]; !ok {
			t.Errorf("%s declares %s, but Dockerfile has no `ARG %s` — the pin can never reach the build", toolVersionsPath, k, k)
		}
	}
	for _, name := range argVersionNames {
		if _, ok := versions[name]; !ok {
			t.Errorf("Dockerfile declares `ARG %s`, but %s has no %s= entry — this ARG can never be given a value", name, toolVersionsPath, name)
		}
	}

	// Also assert the guard RUN actually mentions every one of these names,
	// so a build without --build-arg fails loudly instead of silently
	// installing an empty version (ctxloom's characteristic bug: exit 0,
	// wrong bytes, no error).
	raw, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("read %s: %v", dockerfilePath, err)
	}
	content := string(raw)
	for _, k := range fileKeys {
		needle := "${" + k + ":?"
		if !strings.Contains(content, needle) {
			t.Errorf("Dockerfile has no fail-loud guard (%q) for %s — a bare `docker build` with a missing --build-arg would silently proceed with an empty version instead of failing", needle, k)
		}
	}
}

// TestJustfileDevImageDerivesFromToolVersionsEnv asserts the justfile's
// dev-image recipe (the one `docker build` entry point every local target
// funnels through) reads tool-versions.env and turns it into --build-arg
// flags, rather than hardcoding build-args or defaulting to the Dockerfile's
// (nonexistent) ARG defaults.
func TestJustfileDevImageDerivesFromToolVersionsEnv(t *testing.T) {
	raw, err := os.ReadFile(justfilePath)
	if err != nil {
		t.Fatalf("read %s: %v", justfilePath, err)
	}
	content := string(raw)

	recipeStart := strings.Index(content, "dev-image:")
	if recipeStart < 0 {
		t.Fatalf("%s: no `dev-image:` recipe found", justfilePath)
	}
	// The recipe body runs to the next non-indented line (a new recipe or
	// variable). Grab a generous window after the header.
	body := content[recipeStart:]
	if end := strings.Index(body, "\n\n"); end > 0 {
		body = body[:end]
	}

	if !strings.Contains(body, "tool-versions.env") {
		t.Errorf("justfile's dev-image recipe does not reference .devcontainer/tool-versions.env — it can't be deriving build-args from it:\n%s", body)
	}
	if !strings.Contains(body, "--build-arg") {
		t.Errorf("justfile's dev-image recipe does not pass --build-arg — the Dockerfile's default-less tool version ARGs would resolve empty:\n%s", body)
	}
}

// TestCIWorkflowBuildContainerDerivesFromToolVersionsEnv asserts ci.yml's
// build-container job — which builds .devcontainer/Dockerfile directly via
// docker/build-push-action, NOT through `just dev-image` — also derives its
// build-args from tool-versions.env instead of a separate hardcoded list.
//
// Two hops, since the derivation moved into a recipe: the job must run
// `just tool-version-args` and feed its output to build-args, and that recipe
// must read tool-versions.env. Asserting only the first would pass on a recipe
// that emitted a hardcoded list; asserting only the second would pass on a
// recipe nothing calls.
func TestCIWorkflowBuildContainerDerivesFromToolVersionsEnv(t *testing.T) {
	job := workflowJob(t, ciWorkflowPath, "build-container")

	assertInvokes(t, ciWorkflowPath, job, "tool-version-args")
	if !strings.Contains(job, "docker/build-push-action") {
		t.Fatalf("ci.yml's build-container job no longer uses docker/build-push-action — this test needs updating for whatever replaced it")
	}
	if !strings.Contains(job, "build-args:") {
		t.Errorf("ci.yml's build-container job's docker/build-push-action step has no build-args input — the Dockerfile's default-less tool version ARGs would resolve empty")
	}
	if !strings.Contains(job, "steps.tool_versions.outputs.args") {
		t.Errorf("ci.yml's build-container job does not feed the `tool-version-args` step output into build-args — the recipe would run and its output be discarded")
	}

	body := recipeBody(t, ciJustfilePath, "tool-version-args")
	if !regexp.MustCompile(`(?m)^\s*\w+="\$\([^\n]*tool-versions\.env`).MatchString(body) {
		t.Errorf("build/ci.justfile's tool-version-args recipe does not read .devcontainer/tool-versions.env — it can't be deriving build-args from it:\n%s", body)
	}
}

// workflowJob returns the text of one top-level job in a workflow, from its
// `  name:` line up to the next job's.
func workflowJob(t *testing.T, workflowPath, name string) string {
	t.Helper()
	content := readFile(t, workflowPath)
	header := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + `:\s*$`)
	loc := header.FindStringIndex(content)
	if loc == nil {
		t.Fatalf("%s: no %s job found", workflowPath, name)
	}
	job := content[loc[0]:]
	nextJob := regexp.MustCompile(`(?m)^  [a-zA-Z][\w-]*:\s*$`)
	if next := nextJob.FindStringIndex(job[loc[1]-loc[0]:]); next != nil {
		job = job[:loc[1]-loc[0]+next[0]]
	}
	return job
}

// workflowJobsUsing returns the names of the jobs in a workflow's `jobs:`
// section whose text mentions needle.
func workflowJobsUsing(t *testing.T, workflowPath, needle string) []string {
	t.Helper()
	content := readFile(t, workflowPath)
	jobs := regexp.MustCompile(`(?m)^jobs:\s*$`).FindStringIndex(content)
	if jobs == nil {
		t.Fatalf("%s: no jobs: section", workflowPath)
	}
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^  ([a-zA-Z][\w-]*):\s*$`).FindAllStringSubmatch(content[jobs[1]:], -1) {
		if strings.Contains(workflowJob(t, workflowPath, m[1]), needle) {
			names = append(names, m[1])
		}
	}
	return names
}

// TestCIWorkflowNativeJobsDeriveGoFromToolVersionsEnv asserts every ci.yml
// job that installs Go itself — the native-OS jobs, which cannot run the Linux
// devcontainer image — takes the Go version from tool-versions.env, the file
// the image's Go comes from. Two hops, like the build-container test: the job
// runs `just ci-go-version` and feeds its output to setup-go, and that recipe
// reads GO_VERSION from the file.
func TestCIWorkflowNativeJobsDeriveGoFromToolVersionsEnv(t *testing.T) {
	jobs := workflowJobsUsing(t, ciWorkflowPath, "actions/setup-go")
	if len(jobs) == 0 {
		t.Fatalf("no ci.yml job uses actions/setup-go — this test needs updating for whatever replaced it")
	}
	for _, name := range jobs {
		job := workflowJob(t, ciWorkflowPath, name)
		assertInvokes(t, ciWorkflowPath, job, "ci-go-version")
		if !regexp.MustCompile(`go-version:\s*\$\{\{\s*steps\.go_version\.outputs\.version\s*\}\}`).MatchString(job) {
			t.Errorf("ci.yml's %s job does not feed the `ci-go-version` step output to setup-go's go-version — the recipe would run and its output be discarded", name)
		}
	}

	// An assignment from a command reading GO_VERSION out of the file: a bare
	// mention would also match the recipe's own error message.
	body := recipeBody(t, ciJustfilePath, "ci-go-version")
	if !regexp.MustCompile(`(?m)^\s*\w+="\$\([^\n]*GO_VERSION[^\n]*tool-versions\.env`).MatchString(body) {
		t.Errorf("build/ci.justfile's ci-go-version recipe does not read GO_VERSION from .devcontainer/tool-versions.env:\n%s", body)
	}
}

// TestReleaseCompleterDerivesToolVersionsFromFile is the headline test:
// .github/workflows/release-completer.yml runs in the official Go image, not
// the devcontainer image, and so must install goreleaser/buf/protoc-gen-go/
// protoc-gen-go-grpc/versionator itself. Those installs must read tool-versions.env and reference
// each tool through the shared variable name rather than a hand-copied
// literal. This is precisely the class of drift that shipped once already (see
// package doc comment): a hardcoded version can silently stop matching the
// Dockerfile's, and nothing catches it until a release's codegen breaks.
//
// The installs now live in `release-install-tools` (build/ci.justfile), so
// this checks both hops — the workflow invokes the recipe, the recipe derives
// from the file. Note the recipe SOURCES tool-versions.env directly instead of
// routing it through $GITHUB_ENV, which is why that hop is gone: with the
// installs in one recipe there is no longer a second step that needs the
// values in its environment.
func TestReleaseCompleterDerivesToolVersionsFromFile(t *testing.T) {
	content := readFile(t, releaseWorkflowPath)
	assertInvokes(t, releaseWorkflowPath, content, "release-install-tools")

	body := recipeBody(t, ciJustfilePath, "release-install-tools")
	if !strings.Contains(body, "tool-versions.env") {
		t.Fatalf("build/ci.justfile's release-install-tools recipe does not read .devcontainer/tool-versions.env — it has reverted to hand-copied version pins:\n%s", body)
	}
	for _, key := range releaseCompleterKeys {
		ref := "${" + key + "}"
		if !strings.Contains(body, ref) {
			t.Errorf("build/ci.justfile's release-install-tools recipe does not reference %s — it must be installing this tool from a hardcoded literal instead of the shared version file (the exact drift that shipped once already; see fe0e322 / commit b334605 history)", ref)
		}
	}
}

// TestSetupJustActionDerivesFromToolVersionsEnv covers the consumer the
// all-steps-are-just-targets rework added: runners outside the devcontainer
// image have to install `just` before they can run a single CI step, and if
// that bootstrap picked a different version from the image's, CI would be
// running two different task runners over the same recipes. The composite
// action reads JUST_VERSION from the same file the Dockerfile does.
func TestSetupJustActionDerivesFromToolVersionsEnv(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)
	if _, ok := versions["JUST_VERSION"]; !ok {
		t.Fatalf("%s has no JUST_VERSION — every workflow step is a `just` target, so the version running them must be pinned like any other tool", toolVersionsPath)
	}

	action := readFile(t, setupJustActionPath)
	if !strings.Contains(action, "tool-versions.env") {
		t.Errorf("%s does not read .devcontainer/tool-versions.env — a runner could install a different `just` from the devcontainer image's", setupJustActionPath)
	}
	if !strings.Contains(action, "JUST_VERSION") {
		t.Errorf("%s does not reference JUST_VERSION — it must be installing an unpinned `just`", setupJustActionPath)
	}

	dockerfile := readFile(t, dockerfilePath)
	if !strings.Contains(dockerfile, "${JUST_VERSION}") {
		t.Errorf("Dockerfile never references ${JUST_VERSION} in a RUN body — the image would install an unpinned `just` while runners install a pinned one")
	}
}

// TestDockerfileAndReleaseInstallToolsShareVariableNames ties the two install
// paths together directly: for every tool the release job must install itself,
// the Dockerfile's install RUN for that same tool must reference the IDENTICAL
// variable name. Since both derive from the identical tool-versions.env entry,
// this is what makes disagreement structurally impossible rather than merely
// checked-for after the fact.
func TestDockerfileAndReleaseInstallToolsShareVariableNames(t *testing.T) {
	dockerContent := readFile(t, dockerfilePath)
	recipe := recipeBody(t, ciJustfilePath, "release-install-tools")

	for _, key := range releaseCompleterKeys {
		ref := "${" + key + "}"
		if !strings.Contains(dockerContent, ref) {
			t.Errorf("Dockerfile never references %s in a RUN body — the ARG is declared but unused", ref)
		}
		if !strings.Contains(recipe, ref) {
			t.Errorf("build/ci.justfile's release-install-tools recipe never references %s — see TestReleaseCompleterDerivesToolVersionsFromFile", ref)
		}
	}
}

// stripJSONCLineComments removes standalone `// ...` comment lines so
// encoding/json (which doesn't accept JSONC) can parse a devcontainer.json.
// devcontainer.json is specified as JSONC; this repo's file only uses
// whole-line comments (never a trailing `// ...` after a value on the same
// line), so trimming lines whose first non-whitespace characters are `//` is
// sufficient — it deliberately does NOT handle comments embedded inside
// string values or same-line trailing comments, since neither appears here.
func stripJSONCLineComments(raw string) string {
	lines := strings.Split(raw, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestDevcontainerJSONBuildArgsMatchToolVersionsEnv covers the fourth
// consumer this package found beyond the two named in the original bug
// report: .devcontainer/devcontainer.json's build.args. VS Code's own
// "Reopen in Container" invokes `docker build` directly (never through `just
// dev-image`), so it needs the same tool versions passed explicitly or the
// Dockerfile's default-less ARGs fail its guard RUN. JSON can't shell out to
// read tool-versions.env at build time the way justfile/CI workflows can, so
// these values are a literal, drift-gated copy — this test is what makes
// that copy honest.
func TestDevcontainerJSONBuildArgsMatchToolVersionsEnv(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)

	raw, err := os.ReadFile(devcontainerJSONPath)
	if err != nil {
		t.Fatalf("read %s: %v", devcontainerJSONPath, err)
	}

	var doc struct {
		Build struct {
			Args map[string]string `json:"args"`
		} `json:"build"`
	}
	if err := json.Unmarshal([]byte(stripJSONCLineComments(string(raw))), &doc); err != nil {
		t.Fatalf("parse %s: %v", devcontainerJSONPath, err)
	}

	if len(doc.Build.Args) == 0 {
		t.Fatalf("%s has no build.args — VS Code's own container build will fail the Dockerfile's tool-version guard RUN (no --build-arg supplied)", devcontainerJSONPath)
	}

	for _, k := range imageKeys(versions) {
		want := versions[k]
		got, ok := doc.Build.Args[k]
		if !ok {
			t.Errorf("%s: build.args is missing %s (tool-versions.env has %s=%s)", devcontainerJSONPath, k, k, want)
			continue
		}
		if got != want {
			t.Errorf("%s: build.args.%s=%q disagrees with %s's %s=%q", devcontainerJSONPath, k, got, toolVersionsPath, k, want)
		}
	}
	for k := range doc.Build.Args {
		if _, ok := versions[k]; !ok {
			t.Errorf("%s: build.args has %s, but %s has no such key — stale entry?", devcontainerJSONPath, k, toolVersionsPath)
		}
	}
}

// TestHostInstallRecipesDeriveFromToolVersionsEnv: a host install recipe is
// the only way its tool reaches a developer's machine — lefthook's pre-commit
// secret scan runs whatever gitleaks-install installed, and a host mutation
// run uses whatever test-mutation-install built, which test-mutation-container's
// image must match. A hand-copied version in any of them would let the tool and
// the devcontainer's disagree silently.
func TestHostInstallRecipesDeriveFromToolVersionsEnv(t *testing.T) {
	for _, tc := range []struct{ recipe, versionVar string }{
		{"gitleaks-install", "GITLEAKS_VERSION"},
		{"test-mutation-install", "GOMUTANTS_VERSION"},
		{"test-mutation-container", "GOMUTANTS_VERSION"},
	} {
		t.Run(tc.recipe, func(t *testing.T) {
			body := recipeBody(t, justfilePath, tc.recipe)
			if !strings.Contains(body, "tool-versions.env") {
				t.Fatalf("justfile's %s recipe does not read .devcontainer/tool-versions.env:\n%s", tc.recipe, body)
			}
			if !strings.Contains(body, "${"+tc.versionVar+"}") {
				t.Errorf("justfile's %s recipe does not install at ${%s} — it must be using a hardcoded version:\n%s", tc.recipe, tc.versionVar, body)
			}
		})
	}
}

// TestDevcontainerInstallsTheCommitHookRunner: this project's agent cells are
// built on the devcontainer (an unset isolation_base auto-detects it), and the
// commit hooks git runs in a cell are lefthook's — the generated hook falls
// back to an `echo` that exits 0 when lefthook is not on PATH, so without it a
// child's every commit skips archlint, reprise and the rest SILENTLY. The
// image must carry lefthook, at the version tool-versions.env pins.
func TestDevcontainerInstallsTheCommitHookRunner(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)
	if _, ok := versions["LEFTHOOK_VERSION"]; !ok {
		t.Fatalf("%s pins no LEFTHOOK_VERSION", toolVersionsPath)
	}
	if !strings.Contains(readFile(t, dockerfilePath), "github.com/evilmartians/lefthook@v${LEFTHOOK_VERSION}") {
		t.Errorf("%s does not install lefthook at ${LEFTHOOK_VERSION}", dockerfilePath)
	}
}

// TestDevcontainerInstallsTheReprisePreCommitGate: lefthook's pre-commit hook
// runs `reprise check`, and a container agent's commit runs that hook inside
// this image — without reprise every commit there fails with exit 127. reprise
// has no release yet, so the image builds it from the commit tool-versions.env
// pins and must copy the binary into the FINAL stage: a build stage that makes
// it and a final image that lacks it would pass every other check here.
func TestDevcontainerInstallsTheReprisePreCommitGate(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)
	if _, ok := versions["REPRISE_REVISION"]; !ok {
		t.Fatalf("%s pins no REPRISE_REVISION", toolVersionsPath)
	}
	dockerfile := readFile(t, dockerfilePath)
	if !strings.Contains(dockerfile, `git fetch -q --depth 1 https://github.com/ctxloom/reprise "${REPRISE_REVISION}"`) {
		t.Errorf("%s does not build reprise from ${REPRISE_REVISION}", dockerfilePath)
	}
	lastFrom := strings.LastIndex(dockerfile, "\nFROM ")
	if !strings.Contains(dockerfile[lastFrom:], "COPY --from=reprise /usr/local/bin/reprise /usr/local/bin/reprise") {
		t.Errorf("%s's final stage does not copy reprise in from its build stage", dockerfilePath)
	}
}

// TestDevcontainerInstallsVerifiedNode: the opencode bridge plugin ctxloom
// generates is JavaScript, and its wire tests run it under node. They skip
// where node is absent but FAIL under CI, which runs them in this image — so
// the image must carry node, at the version tool-versions.env pins, and must
// check the bytes it downloads: the release's SHASUMS256.txt against its
// signature, and the tarball against that file.
func TestDevcontainerInstallsVerifiedNode(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)
	if _, ok := versions["NODE_VERSION"]; !ok {
		t.Fatalf("%s pins no NODE_VERSION", toolVersionsPath)
	}
	dockerfile := readFile(t, dockerfilePath)
	for _, want := range []string{
		"https://nodejs.org/dist/v${NODE_VERSION}",
		"node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.xz",
		"gpgv --status-fd 1",
		"SHASUMS256.txt.sig SHASUMS256.txt",
		"sha256sum -c",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("%s: the node install lacks %q — node must be the pinned release, its checksum file signature-checked, and the tarball checked against it", dockerfilePath, want)
		}
	}
}

// TestChecksumPinsPairWithAVersionForEveryArch: a checksum pin binds to the
// version beside it, and the image is built for more than one arch, so a tool
// with any checksum must have one per arch — an arch without one would either
// fail its build or, if the Dockerfile ever tolerated an empty value, install
// unverified bytes on that arch only.
func TestChecksumPinsPairWithAVersionForEveryArch(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)
	tools := map[string]bool{}
	for k := range versions {
		m := checksumKeyRE.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		tools[m[1]] = true
		if _, ok := versions[m[1]+"_VERSION"]; !ok {
			t.Errorf("%s: checksum pin %s has no %s_VERSION beside it — nothing says which release it hashes", toolVersionsPath, k, m[1])
		}
	}
	if !tools["GO"] {
		t.Fatalf("%s pins no GO_SHA256_<ARCH> — the Go toolchain download would be unverified", toolVersionsPath)
	}
	for tool := range tools {
		for _, arch := range imageArches {
			if _, ok := versions[tool+"_SHA256_"+arch]; !ok {
				t.Errorf("%s: %s has checksums but none for %s (%s_SHA256_%s)", toolVersionsPath, tool, arch, tool, arch)
			}
		}
	}
}

// TestRunnerOnlyChecksumPinsAreConsumed: a checksum pin for a platform the
// image is not built for is exempt from the Dockerfile and devcontainer.json
// rules above, so it is held to its own: a runner install must read it. One
// nothing reads looks like a verified download that is not.
func TestRunnerOnlyChecksumPinsAreConsumed(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)
	var consumers []string
	for _, path := range runnerOnlyPinConsumers {
		consumers = append(consumers, readFile(t, path))
	}
	for k := range versions {
		if !isRunnerOnlyPin(k) {
			continue
		}
		if !strings.Contains(strings.Join(consumers, "\n"), k) {
			t.Errorf("%s: %s pins a platform the image is not built for, and none of %v reads it", toolVersionsPath, k, runnerOnlyPinConsumers)
		}
	}
}

// TestDevcontainerInstallsVerifiedGo: the Go toolchain is the image's most
// load-bearing download — every binary the image builds goes through it — so
// its bytes are checked against the sha256 tool-versions.env pins before they
// are extracted. A `curl | tar` would extract whatever arrived; the tarball
// must land in a file, pass `sha256sum -c`, and only then be unpacked.
func TestDevcontainerInstallsVerifiedGo(t *testing.T) {
	step := goInstallStep(t)

	t.Run("pinned release checked against its per-arch sha256", func(t *testing.T) {
		for _, want := range []string{
			"https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz",
			"${GO_SHA256_AMD64}",
			"${GO_SHA256_ARM64}",
			"sha256sum -c",
		} {
			if !strings.Contains(step, want) {
				t.Errorf("%s: the Go install lacks %q — the toolchain must be the pinned release, checked against its pinned sha256 for the build's arch", dockerfilePath, want)
			}
		}
	})

	t.Run("never piped into tar", func(t *testing.T) {
		if regexp.MustCompile(`\|\s*tar\b`).MatchString(step) {
			t.Errorf("%s: the Go install pipes the download into tar — it is extracted before (or without) being verified:\n%s", dockerfilePath, step)
		}
	})

	t.Run("verified before extracted", func(t *testing.T) {
		verify := strings.Index(step, "sha256sum -c")
		extract := strings.Index(step, "tar -C /usr/local")
		if extract < 0 || verify < 0 || extract < verify {
			t.Errorf("%s: the Go install must verify (sha256sum -c) BEFORE extracting (tar -C /usr/local):\n%s", dockerfilePath, step)
		}
	})
}

// goInstallStep returns the Dockerfile's Go install step, from its
// `# Install Go` comment up to the next ENV, with comment lines dropped so the
// checks judge the instructions rather than the prose explaining them.
func goInstallStep(t *testing.T) string {
	t.Helper()
	dockerfile := readFile(t, dockerfilePath)
	start := strings.Index(dockerfile, "# Install Go")
	if start < 0 {
		t.Fatalf("%s: no `# Install Go` step", dockerfilePath)
	}
	step := dockerfile[start:]
	if end := strings.Index(step, "\nENV "); end > 0 {
		step = step[:end]
	}
	var code []string
	for _, line := range strings.Split(step, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code = append(code, line)
		}
	}
	return strings.Join(code, "\n")
}
