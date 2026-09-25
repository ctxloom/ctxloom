package companions

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// companionReleaseSuffix is the release statement's extension. The detached
// signature (companionSigSuffix) is over the statement, not over the binary:
// the statement names the binary and hashes it, so one signature binds WHICH
// program these bytes are as well as the bytes.
const companionReleaseSuffix = ".release"

// companionReleaseMarker opens a companion release statement. It is not the
// bundle manifest's marker, so neither can be replayed as the other.
const companionReleaseMarker = "# ctxloom-companion/1"

// errCompanionRelease is every way a statement fails to be the canonical one.
var errCompanionRelease = errors.New("malformed companion release statement")

// companionRelease is what a companion publisher signs:
//
//	# ctxloom-companion/1
//	# name: <bin>
//	# version: <strict semver>
//	<64 lowercase hex>  <bin>
//
// The entry line is the coreutils SHA256SUMS shape, so `sha256sum -c` checks
// an installed binary against it.
type companionRelease struct {
	name    string
	version *semver.Version
	sha256  string
}

var companionHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (r companionRelease) render() []byte {
	return []byte(fmt.Sprintf("%s\n# name: %s\n# version: %s\n%s  %s\n",
		companionReleaseMarker, r.name, r.version, r.sha256, r.name))
}

// parseCompanionRelease reads a statement STRICTLY: it re-renders what it
// parsed and requires byte equality, so the bytes the signature covers are the
// only reading there is.
func parseCompanionRelease(raw []byte) (companionRelease, error) {
	lines := strings.SplitAfter(string(raw), "\n")
	if len(lines) != 5 || lines[4] != "" {
		return companionRelease{}, fmt.Errorf("%w: want exactly four newline-terminated lines", errCompanionRelease)
	}
	if lines[0] != companionReleaseMarker+"\n" {
		return companionRelease{}, fmt.Errorf("%w: first line is %q, this build understands only %q", errCompanionRelease, strings.TrimSuffix(lines[0], "\n"), companionReleaseMarker)
	}
	name, err := parseReleaseName(lines[1])
	if err != nil {
		return companionRelease{}, err
	}
	vs, ok := strings.CutPrefix(strings.TrimSuffix(lines[2], "\n"), "# version: ")
	if !ok {
		return companionRelease{}, fmt.Errorf("%w: third line must be \"# version: <semver>\"", errCompanionRelease)
	}
	v, err := semver.StrictNewVersion(vs)
	if err != nil {
		return companionRelease{}, fmt.Errorf("%w: version %q is not strict semver: %w", errCompanionRelease, vs, err)
	}
	hash, _, _ := strings.Cut(lines[3], "  ")
	if !companionHash.MatchString(hash) {
		return companionRelease{}, fmt.Errorf("%w: the entry line must start with the binary's 64-hex sha256", errCompanionRelease)
	}
	out := companionRelease{name: name, version: v, sha256: hash}
	if !bytes.Equal(out.render(), raw) {
		return companionRelease{}, fmt.Errorf("%w: not in canonical form (the entry must name %q, two spaces after the hash)", errCompanionRelease, name)
	}
	return out, nil
}

// parseReleaseName reads the statement's name line: a bare binary file name,
// no path separators or spaces.
func parseReleaseName(line string) (string, error) {
	name, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "# name: ")
	if !ok || name == "" || strings.ContainsAny(name, `/\ `) {
		return "", fmt.Errorf("%w: second line must be \"# name: <binary file name>\"", errCompanionRelease)
	}
	return name, nil
}
