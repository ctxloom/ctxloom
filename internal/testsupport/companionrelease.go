package testsupport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// CompanionReleaseStatement renders the `<bin>.release` statement a companion
// publisher signs: the binary's name and version, and the hash of its bytes.
// It is the fixture-side spelling of the format companion admission parses; a
// drift between the two fails every companion test that signs through it.
func CompanionReleaseStatement(name, version string, binary []byte) []byte {
	sum := sha256.Sum256(binary)
	return []byte(fmt.Sprintf("# ctxloom-companion/1\n# name: %s\n# version: %s\n%s  %s\n",
		name, version, hex.EncodeToString(sum[:]), name))
}
