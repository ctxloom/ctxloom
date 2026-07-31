package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pkgSource returns this package's own source text, located from the
// compiled-in path of this test file rather than the process cwd. A
// source-scanning assertion rooted at "." reads nothing once anything moves the
// working directory, matches nothing, and still exits 0 — it evaporates instead
// of failing.
func pkgSource(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller could not resolve this test's own source path")
	b, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), name))
	require.NoError(t, err)
	require.NotEmpty(t, b, "read an empty %s — the scan is looking at the wrong file", name)
	return string(b)
}

// TestAllowedSignersFile_IsExtensionless pins the invariant ApprovalsPath's
// corrected doc now asserts. The doc used to place the approvals directory
// "next to allowed_signers.yaml", a file that has never existed: the trust root
// is deliberately extensionless because its contents are OpenSSH's
// allowed_signers format, not ctxloom's YAML, and it must stay hand-editable by
// anyone who knows ssh-keygen(1). A reader who trusted the comment would look
// for the wrong filename on disk, and anyone "fixing" the constant to match it
// would silently move the trust root.
func TestAllowedSignersFile_IsExtensionless(t *testing.T) {
	assert.Equal(t, "allowed_signers", AllowedSignersFileName)
	assert.Empty(t, filepath.Ext(AllowedSignersFileName),
		"the trust root is OpenSSH's format, not ctxloom's; giving it an extension "+
			"would both misdescribe it and relocate the file every deployment reads")

	assert.Equal(t, ".ctxloom/allowed_signers", AllowedSignersPath(".ctxloom"))

	// "Next to" is the other half of the claim: both live directly at the app
	// dir root, so the approvals store really is the trust root's sibling.
	assert.Equal(t,
		filepath.Dir(AllowedSignersPath("/project/.ctxloom")),
		filepath.Dir(ApprovalsPath("/project/.ctxloom")),
		"the approvals store and the trust root are siblings at the app dir root")
}

// TestPackageDocs_NameNoNonexistentTrustRootFile is the drivable-red half: the
// defect was prose, so this asserts the prose. allowed_signers.yaml is not a
// path this package can produce, so naming it in a doc comment sends the reader
// looking for a file that is not there.
func TestPackageDocs_NameNoNonexistentTrustRootFile(t *testing.T) {
	src := pkgSource(t, "paths.go")

	require.Contains(t, src, "AllowedSignersFileName",
		"the scan did not read this package's source — every assertion below would pass vacuously")

	// Report the OFFENDING LINES, never the whole file: a bare NotContains on
	// the source dumps every byte of paths.go into the failure output, which
	// buries the one line at fault.
	var offenders []string
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, AllowedSignersFileName+".yaml") {
			offenders = append(offenders, fmt.Sprintf("paths.go:%d: %s", i+1, strings.TrimSpace(line)))
		}
	}
	assert.Empty(t, offenders,
		"these lines name %s.yaml, a path this package never builds; the trust root is "+
			"extensionless (see AllowedSignersFileName)", AllowedSignersFileName)
}
