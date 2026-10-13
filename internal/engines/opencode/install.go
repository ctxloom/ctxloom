package opencode

import (
	"fmt"
	"slices"
	"strings"
)

// releaseVersion is the opencode release the container image installs: the
// tested-version lock (.github/engine-versions.env OPENCODE_CLI_VERSION),
// which the drift workflow moves; releaseSHA256 moves with it.
const releaseVersion = "1.18.35"

// releaseURL is where a release's tarballs are published.
const releaseURL = "https://github.com/anomalyco/opencode/releases/download"

// releaseSHA256 pins each linux tarball of releaseVersion by its sha256,
// keyed by asset name (without .tar.gz). The release publishes no checksum
// asset, so the pin in code is the only check a download gets. x64 takes the
// -baseline build, which runs on every x64 CPU (the plain one needs AVX2);
// -musl is the build for a musl libc base.
//
// An EMPTY pin is unverified: the image is not offered (Container refuses)
// until every asset carries one. Fill them from a verified download of the
// release's own tarballs, never from a mirror.
var releaseSHA256 = map[string]string{
	"opencode-linux-x64-baseline":      "",
	"opencode-linux-x64-baseline-musl": "",
	"opencode-linux-arm64":             "",
	"opencode-linux-arm64-musl":        "",
}

// errUnpinned refuses an install whose tarball pins are incomplete or
// malformed.
type errUnpinned struct{ asset, reason string }

func (e errUnpinned) Error() string {
	return fmt.Sprintf("opencode %s: %s's sha256 pin %s", releaseVersion, e.asset, e.reason)
}

// installFragment is the RUN layer that installs opencode version from its
// release tarball on an arbitrary linux base: the asset for the machine and
// libc, downloaded, checked against its pinned sha256, unpacked onto PATH and
// RUN (`opencode --version`) as the validate gate. curl, sha256sum and tar
// come from the base (the base contract layer installs curl); a base missing
// one fails the build naming it. No node, no install script, nothing in HOME.
func installFragment(version string, pins map[string]string) ([]byte, error) {
	assets := slices.Sorted(func(yield func(string) bool) {
		for a := range pins {
			if !yield(a) {
				return
			}
		}
	})
	var arms strings.Builder
	for _, a := range assets {
		sum := pins[a]
		if !isSHA256(sum) {
			reason := "is empty"
			if sum != "" {
				reason = fmt.Sprintf("%q is not 64 lowercase hex digits", sum)
			}
			return nil, errUnpinned{asset: a, reason: reason}
		}
		fmt.Fprintf(&arms, "         %s) sum=%s ;; \\\n", a, sum)
	}
	return fmt.Appendf(nil, `RUN set -e \
    && for t in curl sha256sum tar; do command -v "$t" >/dev/null 2>&1 || { echo "ctxloom: installing opencode needs $t in the base image" >&2; exit 1; }; done \
    && case "$(uname -m)" in \
         x86_64|amd64) asset=opencode-linux-x64-baseline ;; \
         aarch64|arm64) asset=opencode-linux-arm64 ;; \
         *) echo "ctxloom: opencode publishes no linux build for $(uname -m)" >&2; exit 1 ;; \
       esac \
    && if ldd --version 2>&1 | grep -qi musl; then asset="$asset-musl"; fi \
    && case "$asset" in \
%s         *) echo "ctxloom: no sha256 is pinned for $asset" >&2; exit 1 ;; \
       esac \
    && tmp="$(mktemp -d)" \
    && curl -fsSL -o "$tmp/opencode.tar.gz" "%s/v%s/$asset.tar.gz" \
    && echo "$sum  $tmp/opencode.tar.gz" | sha256sum -c - \
    && tar -xzf "$tmp/opencode.tar.gz" -C /usr/local/bin opencode \
    && rm -rf "$tmp" \
    && opencode --version
`, arms.String(), releaseURL, version), nil
}

// isSHA256 is a sha256 as sha256sum prints it: 64 lowercase hex digits.
func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
