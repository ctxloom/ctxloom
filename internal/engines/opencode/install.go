package opencode

import "fmt"

// releaseVersion is the opencode release the container image installs: the
// tested-version lock (.github/engine-versions.env OPENCODE_CLI_VERSION),
// which the drift workflow moves.
const releaseVersion = "1.18.35"

// releaseURL is where a release's tarballs are published.
const releaseURL = "https://github.com/anomalyco/opencode/releases/download"

// installFragment is the RUN layer that installs opencode version from its
// release tarball on an arbitrary linux base: the asset for the machine and
// libc, downloaded from the official GitHub release as-is, unpacked onto
// PATH and RUN (`opencode --version`) as the validate gate. x64 takes the
// -baseline build, which runs on every x64 CPU (the plain one needs AVX2);
// -musl is the build for a musl libc base.
//
// There is no checksum step: upstream publishes no checksums, and ctxloom
// does not invent its own attestation, so integrity rests on GitHub's TLS
// and the release itself. curl and tar come from the base (the base contract
// layer installs curl); a base missing one fails the build naming it. No
// node, no install script, nothing in HOME.
func installFragment(version string) []byte {
	return fmt.Appendf(nil, `RUN set -e \
    && for t in curl tar; do command -v "$t" >/dev/null 2>&1 || { echo "ctxloom: installing opencode needs $t in the base image" >&2; exit 1; }; done \
    && case "$(uname -m)" in \
         x86_64|amd64) asset=opencode-linux-x64-baseline ;; \
         aarch64|arm64) asset=opencode-linux-arm64 ;; \
         *) echo "ctxloom: opencode publishes no linux build for $(uname -m)" >&2; exit 1 ;; \
       esac \
    && if ldd --version 2>&1 | grep -qi musl; then asset="$asset-musl"; fi \
    && tmp="$(mktemp -d)" \
    && curl -fsSL -o "$tmp/opencode.tar.gz" "%s/v%s/$asset.tar.gz" \
    && tar -xzf "$tmp/opencode.tar.gz" -C /usr/local/bin opencode \
    && rm -rf "$tmp" \
    && opencode --version
`, releaseURL, version)
}
