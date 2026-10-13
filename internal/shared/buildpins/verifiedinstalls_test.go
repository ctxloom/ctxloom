package buildpins

import (
	"regexp"
	"strings"
	"testing"
)

// Every devcontainer download whose upstream publishes something to check it
// against is checked against it BEFORE it is installed. These tests read the
// Dockerfile step that installs each tool and hold it to that: the pinned
// release, the published checksum or signature, verification ahead of the
// install, and nothing piped from the network straight into a shell or tar.

// dockerfileStepCode returns the Dockerfile step introduced by the comment
// line starting with marker — every line from there to the next blank line —
// with its comment lines dropped, so the instructions are judged rather than
// the prose explaining them.
func dockerfileStepCode(t *testing.T, marker string) string {
	t.Helper()
	dockerfile := readFile(t, dockerfilePath)
	start := strings.Index(dockerfile, "\n"+marker)
	if start < 0 {
		t.Fatalf("%s: no step introduced by %q", dockerfilePath, marker)
	}
	step := dockerfile[start+1:]
	if end := strings.Index(step, "\n\n"); end > 0 {
		step = step[:end]
	}
	return withoutCommentLines(step)
}

// withoutCommentLines drops every line whose first non-blank character is #,
// so a shell step is judged by its instructions, not its prose.
func withoutCommentLines(text string) string {
	var code []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code = append(code, line)
		}
	}
	return strings.Join(code, "\n")
}

var pipeIntoExecRE = regexp.MustCompile(`\|\s*(bash|sh|tar)\b`)

// assertVerifiedStep holds one install step, read from path, to the contract:
// it contains every wanted fragment, pipes nothing into a shell or tar, and
// its fragments in order appear in that order (verify before install).
func assertVerifiedStep(t *testing.T, path, tool, step string, want, order []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(step, w) {
			t.Errorf("%s: the %s install lacks %q:\n%s", path, tool, w, step)
		}
	}
	if pipeIntoExecRE.MatchString(step) {
		t.Errorf("%s: the %s install pipes a download into a shell or tar — it runs or unpacks bytes before (or without) verifying them:\n%s", path, tool, step)
	}
	last := -1
	for _, o := range order {
		i := strings.Index(step, o)
		if i <= last {
			t.Errorf("%s: the %s install must run %q in that order (%q missing or out of place) — verification precedes installation:\n%s", path, tool, order, o, step)
			return
		}
		last = i
	}
}

// buf publishes sha256.txt per release and signs it with minisign; buf's
// installation docs (https://buf.build/docs/cli/installation/, "Verify a
// release with minisign") give the public key and the procedure.
const bufReleaseSigner = "RWQ/i9xseZwBVE7pEniCNjlNOeeyp4BQgdZDLQcAohxEAH5Uj5DEKjv6"

func TestDevcontainerInstallsVerifiedBuf(t *testing.T) {
	step := dockerfileStepCode(t, "# Install buf")
	assertVerifiedStep(t, dockerfilePath, "buf", step,
		[]string{
			`ARG BUF_RELEASE_SIGNER="` + bufReleaseSigner + `"`,
			"https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}",
			"sha256.txt.minisig",
			`minisign -Vm sha256.txt -P "${BUF_RELEASE_SIGNER}"`,
			"sha256sum -c",
			`test "$(buf --version)" = "${BUF_VERSION}"`,
		},
		[]string{"minisign -Vm", "sha256sum -c", "/usr/local/bin/buf"},
	)
}

// just publishes SHA256SUMS per release (no signature); the image pins this
// arch's line of it, like Go, and never pipes just.systems/install.sh to bash.
func TestDevcontainerInstallsVerifiedJust(t *testing.T) {
	step := dockerfileStepCode(t, "# Install just")
	assertVerifiedStep(t, dockerfilePath, "just", step,
		[]string{
			"https://github.com/casey/just/releases/download/${JUST_VERSION}/",
			"${JUST_SHA256_AMD64}",
			"${JUST_SHA256_ARM64}",
			"sha256sum -c",
			`test "$(just --version)" = "just ${JUST_VERSION}"`,
		},
		[]string{"sha256sum -c", "tar -xzf"},
	)
	if strings.Contains(step, "just.systems/install.sh") {
		t.Errorf("%s: just is still installed by just.systems/install.sh, a script fetched at build time:\n%s", dockerfilePath, step)
	}
}

// The runners outside the image bootstrap just through .github/actions/setup-
// just, on Linux, macOS and Windows: each platform's release archive is checked
// against its SHA256SUMS line, pinned in tool-versions.env beside the image's,
// before it is unpacked — never just.systems/install.sh piped to bash.
func TestSetupJustInstallsVerifiedJust(t *testing.T) {
	step := withoutCommentLines(readFile(t, setupJustActionPath))
	assertVerifiedStep(t, setupJustActionPath, "just", step,
		[]string{
			"https://github.com/casey/just/releases/download/${version}/",
			"JUST_SHA256_AMD64",
			"JUST_SHA256_ARM64",
			"JUST_SHA256_DARWINARM64",
			"JUST_SHA256_WINDOWSAMD64",
			`= "just ${version}"`,
		},
		[]string{"-c -", "unzip ", "tar -xzf", `= "just ${version}"`},
	)
	if strings.Contains(step, "just.systems/install.sh") {
		t.Errorf("%s: just is still installed by just.systems/install.sh, a script fetched at run time:\n%s", setupJustActionPath, step)
	}
}

// dockerAptKeyFingerprint is the primary-key fingerprint Docker documented for
// its apt signing key ("Verify that you now have the key with the fingerprint
// 9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88" — docs.docker.com/engine/
// install/ubuntu/, docker/docs engine/install/ubuntu.md before 760bb64ea3cc).
const dockerAptKeyFingerprint = "9DC858229FC7DD38854AE2D88D81803C0EBFCD88"

// The Docker apt key is trusted for every docker-ce package apt installs, so
// the fetched key must be exactly Docker's documented key before it is placed
// in /etc/apt/keyrings — as the git PPA key is fetched by full fingerprint.
func TestDevcontainerVerifiesDockerAptKey(t *testing.T) {
	step := dockerfileStepCode(t, "# Install build dependencies")
	assertVerifiedStep(t, dockerfilePath, "Docker apt key", step,
		[]string{
			`ARG DOCKER_APT_FINGERPRINT="` + dockerAptKeyFingerprint + `"`,
			"gpg --batch --show-keys --with-colons",
			`"${DOCKER_APT_FINGERPRINT}"`,
		},
		[]string{"https://download.docker.com/linux/ubuntu/gpg", "--show-keys", "/etc/apt/keyrings/docker.asc"},
	)
}

// lizard and its dependencies come from PyPI under pip's hash-checking mode:
// every distribution must match a sha256 in the requirements file, and pip
// refuses any requirement (a dependency included) that carries none.
func TestDevcontainerInstallsVerifiedLizard(t *testing.T) {
	step := dockerfileStepCode(t, "# Install lizard")
	assertVerifiedStep(t, dockerfilePath, "lizard", step,
		[]string{
			"COPY .devcontainer/lizard-requirements.txt ",
			"--require-hashes -r",
			`test "$(lizard --version)" = "${LIZARD_VERSION}"`,
		},
		[]string{"COPY .devcontainer/lizard-requirements.txt", "--require-hashes"},
	)
	if strings.Contains(step, "lizard==") {
		t.Errorf("%s: lizard is installed by name, outside the hash-pinned requirements file:\n%s", dockerfilePath, step)
	}
}

// serena-agent is installed with `uv tool install`, which (uv 0.9.22) has no
// hash-checking mode: it ignores hashes in --constraints and
// --with-requirements files alike. The version is therefore pinned, and the
// install must prove it got that version — the strongest check uv allows.
func TestDevcontainerInstallsPinnedSerena(t *testing.T) {
	step := dockerfileStepCode(t, "# The tool venv and its shims")
	assertVerifiedStep(t, dockerfilePath, "serena-agent", step,
		[]string{
			`uv tool install --python python3 "serena-agent==${SERENA_AGENT_VERSION}"`,
			`grep -qx "serena-agent v${SERENA_AGENT_VERSION}"`,
		},
		[]string{"uv tool install", "uv tool list", "serena --help"},
	)
}
