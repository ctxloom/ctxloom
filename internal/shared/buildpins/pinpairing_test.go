package buildpins

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pairing rule of scripts/lint-pins, driven over the REAL pin files: for
// every download the image verifies against a pinned hash or digest, a bump
// of its version alone — the hashes left as they were — must be refused at
// commit time, not discovered as a failed (or, worse, "fixed") image build.

// checksumPinnedTools are the tool-versions.env tools whose downloads the
// Dockerfile checks against `<TOOL>_SHA256_<ARCH>`.
var checksumPinnedTools = []string{"GO", "JUST"}

// realPinRepo is a pinRepo whose HEAD carries the real tree's copy of the pin
// file at rel; it returns the repo and that content.
func realPinRepo(t *testing.T, rel, realPath string) (*pinRepo, string) {
	t.Helper()
	r := newPinRepo(t)
	content := readFile(t, realPath)
	r.stage(rel, content)
	r.git("commit", "-q", "-m", "real pins")
	return r, content
}

// bumpLast increments a dotted version's last numeric component.
func bumpLast(t *testing.T, v string) string {
	t.Helper()
	i := strings.LastIndex(v, ".")
	n, err := strconv.Atoi(v[i+1:])
	require.NoError(t, err, "version %q does not end in a number", v)
	return v[:i+1] + strconv.Itoa(n+1)
}

// replaceOnce replaces old with new exactly once, failing if old is absent.
func replaceOnce(t *testing.T, content, old, new string) string {
	t.Helper()
	require.Contains(t, content, old)
	return strings.Replace(content, old, new, 1)
}

func TestLintPins_RealChecksumPins_VersionBumpAloneIsRefused(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)
	for _, tool := range checksumPinnedTools {
		t.Run(tool, func(t *testing.T) {
			r, env := realPinRepo(t, ".devcontainer/tool-versions.env", toolVersionsPath)
			key := tool + "_VERSION"
			v := versions[key]
			r.stage(".devcontainer/tool-versions.env", replaceOnce(t, env, key+"="+v+"\n", key+"="+bumpLast(t, v)+"\n"))
			code, out := r.run("")
			assert.Equal(t, 1, code, "a %s bump with its checksums unchanged passed lint-pins:\n%s", key, out)
			for k := range versions {
				if strings.HasPrefix(k, tool+"_SHA256_") {
					assert.Contains(t, out, k)
				}
			}
		})
	}
}

// Base images are pinned `name:tag@sha256:<index digest>`. scripts/lint-pins
// reads them from the Dockerfile as `image:<name>_VERSION` (the tag) and
// `image:<name>_SHA256_INDEX` (the digest), so the tag and digest pair like a
// version and its checksum.
var (
	dockerFromRE     = regexp.MustCompile(`(?mi)^FROM\s+(?:--\S+\s+)*(\S+)(?:\s+AS\s+(\S+))?`)
	dockerCopyFromRE = regexp.MustCompile(`(?mi)^COPY\s+.*?--from=(\S+)`)
	pinnedImageRE    = regexp.MustCompile(`^([^@\s]+):([^@:/\s]+)@sha256:([0-9a-f]{64})$`)
)

// dockerfileImageRefs returns every image the Dockerfile FROMs or COPYs
// from, skipping references to its own named stages.
func dockerfileImageRefs(t *testing.T, dockerfile string) []string {
	t.Helper()
	stages := map[string]bool{}
	var refs []string
	for _, m := range dockerFromRE.FindAllStringSubmatch(dockerfile, -1) {
		refs = append(refs, m[1])
		if m[2] != "" {
			stages[m[2]] = true
		}
	}
	for _, m := range dockerCopyFromRE.FindAllStringSubmatch(dockerfile, -1) {
		refs = append(refs, m[1])
	}
	var images []string
	for _, ref := range refs {
		if !stages[ref] {
			images = append(images, ref)
		}
	}
	require.NotEmpty(t, images, "%s: no FROM image found", dockerfilePath)
	return images
}

// dockerfileImagePins is what lint-pins --list should report for the real
// Dockerfile's images.
func dockerfileImagePins(t *testing.T) map[string]string {
	t.Helper()
	pins := map[string]string{}
	for _, ref := range dockerfileImageRefs(t, readFile(t, dockerfilePath)) {
		if m := pinnedImageRE.FindStringSubmatch(ref); m != nil {
			pins["image:"+m[1]+"_VERSION"] = m[2]
			pins["image:"+m[1]+"_SHA256_INDEX"] = m[3]
		}
	}
	return pins
}

// TestDockerfileImagesArePinnedByDigest: a tag is a name the registry can
// point anywhere; the digest is the bytes. Every image the devcontainer is
// built from carries both.
func TestDockerfileImagesArePinnedByDigest(t *testing.T) {
	for _, ref := range dockerfileImageRefs(t, readFile(t, dockerfilePath)) {
		if !pinnedImageRE.MatchString(ref) {
			t.Errorf("%s: image %q is not pinned as name:tag@sha256:<64 hex> — a tag alone floats", dockerfilePath, ref)
		}
	}
}

func TestLintPins_RealImagePins_TagBumpAloneIsRefused(t *testing.T) {
	for _, ref := range dockerfileImageRefs(t, readFile(t, dockerfilePath)) {
		t.Run(ref, func(t *testing.T) {
			m := pinnedImageRE.FindStringSubmatch(ref)
			require.NotNil(t, m, "image %q carries no digest to go stale", ref)
			r, df := realPinRepo(t, ".devcontainer/Dockerfile", dockerfilePath)
			r.stage(".devcontainer/Dockerfile", replaceOnce(t, df, m[1]+":"+m[2]+"@", m[1]+":"+m[2]+"-next@"))
			code, out := r.run("")
			assert.Equal(t, 1, code, "a %s tag bump with its digest unchanged passed lint-pins:\n%s", m[1], out)
			assert.Contains(t, out, "image:"+m[1]+"_SHA256_INDEX")
			assert.Contains(t, out, "stale")
		})
	}
}

// A tag is mutable by design (ubuntu:24.04 is re-pushed with every security
// refresh), so moving the digest under an unchanged tag is the routine bump,
// not the "different download under the same version" a tarball checksum
// guards against.
func TestLintPins_ImageDigestRefreshUnderSameTag_Passes(t *testing.T) {
	r := newPinRepo(t)
	r.stage(".devcontainer/Dockerfile", "FROM ubuntu:24.04@sha256:"+goSHAAmd64Old+"\n")
	r.git("commit", "-q", "-m", "pin image")
	r.stage(".devcontainer/Dockerfile", "FROM ubuntu:24.04@sha256:"+goSHAAmd64New+"\n")
	code, out := r.run("")
	assert.Equal(t, 0, code, out)
}

// lizard is pip-installed with --require-hashes from a requirements file that
// pins it and its runtime dependencies, one requirement per line:
// `name==version --hash=sha256:<wheel> --hash=sha256:<sdist>`. lint-pins reads
// each as `pip:<name>_VERSION` plus `pip:<name>_SHA256_<n>`.
const lizardRequirementsPath = "../../../.devcontainer/lizard-requirements.txt"

var pipRequirementRE = regexp.MustCompile(`^([a-z0-9._-]+)==([0-9][^\s]*)((?:\s+--hash=sha256:[0-9a-f]{64})+)\s*$`)

type pipRequirement struct {
	name, version string
	hashes        []string
}

func parseLizardRequirements(t *testing.T) []pipRequirement {
	t.Helper()
	var reqs []pipRequirement
	for _, line := range strings.Split(readFile(t, lizardRequirementsPath), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := pipRequirementRE.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("%s: %q is not `name==version --hash=sha256:<64 hex>...` on one line (lowercase name, no continuations)", lizardRequirementsPath, line)
			continue
		}
		reqs = append(reqs, pipRequirement{m[1], m[2], strings.Fields(strings.ReplaceAll(m[3], "--hash=sha256:", ""))})
	}
	return reqs
}

// lizardRequirementPins is what lint-pins --list should report for the file.
func lizardRequirementPins(t *testing.T) map[string]string {
	t.Helper()
	pins := map[string]string{}
	for _, r := range parseLizardRequirements(t) {
		pins["pip:"+r.name+"_VERSION"] = r.version
		for i, h := range r.hashes {
			pins["pip:"+r.name+"_SHA256_"+strconv.Itoa(i+1)] = h
		}
	}
	return pins
}

// The requirements file is where lizard's version is decided; LIZARD_VERSION
// in tool-versions.env must name the same release, or the two pins disagree
// about what the image contains.
func TestLizardRequirementsPinLizardVersion(t *testing.T) {
	want := parseToolVersionsEnv(t, toolVersionsPath)["LIZARD_VERSION"]
	for _, r := range parseLizardRequirements(t) {
		if r.name == "lizard" {
			assert.Equal(t, want, r.version, "%s pins lizard==%s but tool-versions.env has LIZARD_VERSION=%s", lizardRequirementsPath, r.version, want)
			return
		}
	}
	t.Errorf("%s does not pin lizard", lizardRequirementsPath)
}

func TestLintPins_RealPipPins_VersionBumpAloneIsRefused(t *testing.T) {
	for _, req := range parseLizardRequirements(t) {
		t.Run(req.name, func(t *testing.T) {
			r, reqs := realPinRepo(t, ".devcontainer/lizard-requirements.txt", lizardRequirementsPath)
			old := req.name + "==" + req.version + " "
			r.stage(".devcontainer/lizard-requirements.txt", replaceOnce(t, reqs, old, req.name+"=="+bumpLast(t, req.version)+" "))
			code, out := r.run("")
			assert.Equal(t, 1, code, "a %s bump with its hashes unchanged passed lint-pins:\n%s", req.name, out)
			assert.Contains(t, out, "pip:"+req.name+"_SHA256_1")
			assert.Contains(t, out, "stale")
		})
	}
}
