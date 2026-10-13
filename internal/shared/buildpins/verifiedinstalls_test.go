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
	var code []string
	for _, line := range strings.Split(step, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code = append(code, line)
		}
	}
	return strings.Join(code, "\n")
}

var pipeIntoExecRE = regexp.MustCompile(`\|\s*(bash|sh|tar)\b`)

// assertVerifiedStep holds one install step to the contract: it contains every
// wanted fragment, pipes nothing into a shell or tar, and its fragments in
// order appear in that order (verify before install).
func assertVerifiedStep(t *testing.T, tool, step string, want, order []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(step, w) {
			t.Errorf("%s: the %s install lacks %q:\n%s", dockerfilePath, tool, w, step)
		}
	}
	if pipeIntoExecRE.MatchString(step) {
		t.Errorf("%s: the %s install pipes a download into a shell or tar — it runs or unpacks bytes before (or without) verifying them:\n%s", dockerfilePath, tool, step)
	}
	last := -1
	for _, o := range order {
		i := strings.Index(step, o)
		if i <= last {
			t.Errorf("%s: the %s install must run %q in that order (%q missing or out of place) — verification precedes installation:\n%s", dockerfilePath, tool, order, o, step)
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
	assertVerifiedStep(t, "buf", step,
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
	assertVerifiedStep(t, "just", step,
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
