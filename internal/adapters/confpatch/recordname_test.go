package confpatch

import (
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A §9.7 record is an audit-trail entry, and safefs.WriteFileKeepMode overwrites — so
// the filename is the only thing standing between two applies and the loss of
// the first one's record. At second resolution it was not enough: two applies
// against the same target in the same second produced the same name, and the
// evidence needed to back out the first write was destroyed on a success path.
//
// The clock is fixed here deliberately. With time.Now() inlined, this case
// could only be provoked by racing two applies inside one second, which is not
// something a test can state.
func TestFreeRecordPath_NeverOverwritesAnExistingRecord(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/home/user/.ctxloom/records"
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	target := "/home/user/.config/zed/settings.json"
	at := time.Date(2026, 8, 14, 22, 19, 51, 123456789, time.UTC)

	first, err := FreeRecordPath(fs, dir, target, at)
	require.NoError(t, err)
	testsupport.WriteFile(t, fs, first, []byte("first record\n"), 0o644)

	second, err := FreeRecordPath(fs, dir, target, at)
	require.NoError(t, err)
	assert.NotEqual(t, first, second,
		"a second apply at the SAME instant must not be handed the first record's path")

	testsupport.WriteFile(t, fs, second, []byte("second record\n"), 0o644)

	// The effect, not the report: both records are on disk with their own
	// content. Asserting only that the paths differ would pass against a
	// scheme that returned a fresh name and then clobbered anyway.
	got1, err := afero.ReadFile(fs, first)
	require.NoError(t, err)
	assert.Equal(t, "first record\n", string(got1), "the first record must survive the second apply")
	got2, err := afero.ReadFile(fs, second)
	require.NoError(t, err)
	assert.Equal(t, "second record\n", string(got2))

	// A third at the same instant keeps climbing rather than reusing -2.
	third, err := FreeRecordPath(fs, dir, target, at)
	require.NoError(t, err)
	assert.NotEqual(t, first, third)
	assert.NotEqual(t, second, third)
}

// The timestamp must carry sub-second precision, and must stay lexically
// sortable — a record set is read in apply order.
func TestRecordFilename_IsNanosecondAndSortable(t *testing.T) {
	target := "/home/user/.config/zed/settings.json"
	base := time.Date(2026, 8, 14, 22, 19, 51, 0, time.UTC)

	early := RecordFilename(target, base.Add(1*time.Nanosecond))
	late := RecordFilename(target, base.Add(2*time.Nanosecond))

	assert.NotEqual(t, early, late,
		"two applies one nanosecond apart must not share a filename")
	assert.Less(t, early, late,
		"record filenames must sort in apply order, so the timestamp has to stay lexical")
}

func TestRecordFilename_FitsNameMaxWithAtomicWriteHeadroom(t *testing.T) {
	name := RecordFilename(deepTarget("/", ".mcp.json"), time.Now())
	// safefs.WriteFileKeepMode stages the write as "." + name + "." + <digits> +
	// ".tmp" in the same directory, so the bound has to hold for that name
	// too, not just the final one.
	assert.Less(t, len(name)+len(".")+len(".")+len("123456789")+len(".tmp"), 255)
}
