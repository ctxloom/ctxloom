// Package buildpins is the drift gate for ctxloom's pinned codegen/build
// tool versions. .devcontainer/tool-versions.env is meant to be the ONE
// place these versions are declared; the Dockerfile, the justfile, and
// .github/workflows/{ci,release-completer}.yml are all supposed to derive
// from it instead of hand-copying version numbers.
//
// That "supposed to" is exactly what failed once already: buf.gen.yaml
// switched to pinned `local:` plugins (commit b334605) and
// release-completer.yml — which runs in goreleaser-cross, not the
// devcontainer image, so it can't just inherit the pin — was never updated
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
	"sort"
	"strings"
	"testing"
)

const (
	toolVersionsPath     = "../../.devcontainer/tool-versions.env"
	dockerfilePath       = "../../.devcontainer/Dockerfile"
	justfilePath         = "../../justfile"
	ciWorkflowPath       = "../../.github/workflows/ci.yml"
	releaseWorkflowPath  = "../../.github/workflows/release-completer.yml"
	devcontainerJSONPath = "../../.devcontainer/devcontainer.json"
)

// releaseCompleterKeys are the tool-versions.env keys that
// release-completer.yml must independently install (it runs in
// goreleaser-cross, not the devcontainer image, so it can't just inherit
// them from there). This is the exact set implicated in the historical
// failure this package guards against.
var releaseCompleterKeys = []string{
	"BUF_VERSION",
	"PROTOC_GEN_GO_VERSION",
	"PROTOC_GEN_GO_GRPC_VERSION",
	"VERSIONATOR_VERSION",
}

var versionValueRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)

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
		if !strings.HasSuffix(name, "_VERSION") {
			continue
		}
		argVersionNames = append(argVersionNames, name)
		if arg.hasDefault {
			t.Errorf("Dockerfile: ARG %s has a default value — tool version ARGs must be default-less so %s stays the only source of truth (a default would silently diverge from it)", name, toolVersionsPath)
		}
	}

	var fileKeys []string
	for k := range versions {
		fileKeys = append(fileKeys, k)
	}
	sort.Strings(fileKeys)
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
func TestCIWorkflowBuildContainerDerivesFromToolVersionsEnv(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", ciWorkflowPath, err)
	}
	content := string(raw)

	jobStart := strings.Index(content, "build-container:")
	if jobStart < 0 {
		t.Fatalf("%s: no build-container job found", ciWorkflowPath)
	}
	nextJob := regexp.MustCompile(`(?m)^  [a-zA-Z][\w-]*:\s*$`)
	job := content[jobStart:]
	if loc := nextJob.FindStringIndex(job[len("build-container:"):]); loc != nil {
		job = job[:len("build-container:")+loc[0]]
	}

	if !strings.Contains(job, "tool-versions.env") {
		t.Errorf("ci.yml's build-container job does not reference .devcontainer/tool-versions.env:\n%s", job)
	}
	if !strings.Contains(job, "docker/build-push-action") {
		t.Fatalf("ci.yml's build-container job no longer uses docker/build-push-action — this test needs updating for whatever replaced it")
	}
	if !strings.Contains(job, "build-args:") {
		t.Errorf("ci.yml's build-container job's docker/build-push-action step has no build-args input — the Dockerfile's default-less tool version ARGs would resolve empty")
	}
}

// TestReleaseCompleterDerivesToolVersionsFromFile is the headline test: it
// asserts .github/workflows/release-completer.yml — which runs in
// goreleaser-cross, not the devcontainer image, and so must install buf/
// protoc-gen-go/protoc-gen-go-grpc/versionator itself — both loads
// tool-versions.env AND references each tool's version through the shared
// variable name, rather than a hand-copied literal. This is precisely the
// class of drift that shipped once already (see package doc comment): a
// hardcoded version here can silently stop matching the Dockerfile's, and
// nothing catches it until a release's codegen breaks.
func TestReleaseCompleterDerivesToolVersionsFromFile(t *testing.T) {
	raw, err := os.ReadFile(releaseWorkflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", releaseWorkflowPath, err)
	}
	content := string(raw)

	if !strings.Contains(content, "tool-versions.env") {
		t.Fatalf("%s does not reference .devcontainer/tool-versions.env at all — it has reverted to hand-copied version pins", releaseWorkflowPath)
	}
	if !strings.Contains(content, "GITHUB_ENV") {
		t.Fatalf("%s references tool-versions.env but never loads it into $GITHUB_ENV — later steps can't see the versions", releaseWorkflowPath)
	}

	for _, key := range releaseCompleterKeys {
		ref := "${" + key + "}"
		if !strings.Contains(content, ref) {
			t.Errorf("%s does not reference %s — it must be install-ing this tool from a hardcoded literal instead of the shared version file (the exact drift that shipped once already; see fe0e322 / commit b334605 history)", releaseWorkflowPath, ref)
		}
	}
}

// TestDockerfileAndReleaseCompleterShareVariableNames ties the two files
// together directly: for every tool release-completer.yml must install
// itself, the Dockerfile's install RUN for that same tool must reference the
// IDENTICAL variable name. Since both derive from the identical
// tool-versions.env entry, this is what makes disagreement structurally
// impossible rather than merely checked-for after the fact.
func TestDockerfileAndReleaseCompleterShareVariableNames(t *testing.T) {
	dockerRaw, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("read %s: %v", dockerfilePath, err)
	}
	releaseRaw, err := os.ReadFile(releaseWorkflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", releaseWorkflowPath, err)
	}
	dockerContent := string(dockerRaw)
	releaseContent := string(releaseRaw)

	for _, key := range releaseCompleterKeys {
		ref := "${" + key + "}"
		if !strings.Contains(dockerContent, ref) {
			t.Errorf("Dockerfile never references %s in a RUN body — the ARG is declared but unused", ref)
		}
		if !strings.Contains(releaseContent, ref) {
			t.Errorf("release-completer.yml never references %s — see TestReleaseCompleterDerivesToolVersionsFromFile", ref)
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

	var fileKeys []string
	for k := range versions {
		fileKeys = append(fileKeys, k)
	}
	sort.Strings(fileKeys)

	for _, k := range fileKeys {
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
