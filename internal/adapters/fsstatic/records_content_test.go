package fsstatic

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const notesTarget = "/proj/notes.md"

func recordPath(target string) string {
	return filepath.Join(claimsDir, confpatch.RecordPrefix(target)+claimsSuffix)
}

func whole(b []byte) present.Claim { return present.Claim{Value: b} }

// rawEntry is the first claim at the whole-file place of target's record, as
// the YAML on disk spells it.
func rawEntry(t *testing.T, fs afero.Fs, target string) map[string]any {
	t.Helper()
	var m struct {
		Paths map[string][]map[string]any `yaml:"paths"`
	}
	require.NoError(t, yamlv3.Unmarshal([]byte(read(t, fs, recordPath(target))), &m))
	require.NotEmpty(t, m.Paths[""])
	return m.Paths[""][0]
}

// A delivered text file is kept in its record as text: the record is about
// the size of the file, not one YAML integer per byte.
func TestClaimsRecord_DeliveredTextIsStoredAsText(t *testing.T) {
	line := "- a line of delivered markdown, ordinary prose with `code` in it\n"
	text := strings.Repeat(line, 22718/len(line)+1)
	fs := afero.NewMemMapFs()
	c := newRecords(t, fs)
	mustCommit(t, c, fs, stage(notesTarget, project, whole([]byte(text))))

	size := len(read(t, fs, recordPath(notesTarget)))
	t.Logf("a %d-byte text file records %d bytes", len(text), size)
	assert.Less(t, size, len(text)*5/4, "a %d-byte text file's record is %d bytes", len(text), size)
	assert.Equal(t, text, rawEntry(t, fs, notesTarget)["content"])
}

// Whatever a file holds, the record gives back exactly those bytes: text
// with awkward whitespace and controls, and bytes that are not UTF-8 at all.
func TestClaimsRecord_ContentRoundTripsByteExactly(t *testing.T) {
	for name, content := range map[string][]byte{
		"text":                []byte("# title\n\nbody\n"),
		"no trailing newline": []byte("one line"),
		"edge whitespace":     []byte("  leading\n\n\ttabbed\ntrailing  \n\n\n"),
		"crlf and controls":   []byte("a\r\nb\x00c\x1b[0m\r\n"),
		"yaml-looking":        []byte("key: value\n- item\n---\n"),
		"not utf-8":           {0xff, 0xfe, 'h', 'i', 0x80, '\n', 0xc3},
	} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			mustCommit(t, newRecords(t, fs), fs, stage(notesTarget, project, whole(content)))
			before := read(t, fs, recordPath(notesTarget))

			rec, err := decodeClaims(recordPath(notesTarget), []byte(before))
			require.NoError(t, err)
			require.Len(t, rec.Paths[""], 1)
			assert.Equal(t, content, []byte(rec.Paths[""][0].Content))

			// A fresh store reads it back as live, and restating it changes
			// neither the file nor the record.
			c := newRecords(t, fs)
			states, err := c.Paths(fs, notesTarget)
			require.NoError(t, err)
			require.Len(t, states, 1)
			assert.True(t, states[0].Live)
			mustCommit(t, c, fs, stage(notesTarget, project, whole(content)))
			assert.Equal(t, content, []byte(read(t, fs, notesTarget)))
			assert.Equal(t, before, read(t, fs, recordPath(notesTarget)))
		})
	}
}

// intList spells b as the integer list the generation-2 record wrote.
func intList(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = strconv.Itoa(int(x))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// A generation-2 record spelled delivered bytes as an integer list, in the
// claims and in the pending note's prior state. It reads as the same claims,
// is not rewritten by a read, and the next write stores the new form.
func TestClaimsRecord_IntegerListRecordMigratesInMemory(t *testing.T) {
	content := []byte("hi\n\xff")
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, notesTarget, string(content), 0o644)
	body := "claims: 2\ntarget: " + notesTarget + "\ncreated: true\nseq: 1\npaths:\n" +
		"  \"\":\n    - writer: project\n      seq: 1\n      value: null\n      bytes: " + intList(content) + "\n" +
		"pending:\n  before: absent\n  after: whatever\n  prior:\n    created: false\n    values:\n" +
		"      \"\":\n        writer: project\n        seq: 1\n        value: null\n        bytes: " + intList(content) + "\n"
	testsupport.WriteFileString(t, fs, recordPath(notesTarget), body, 0o600)

	rec, err := decodeClaims(recordPath(notesTarget), []byte(body))
	require.NoError(t, err)
	assert.Equal(t, content, []byte(rec.Paths[""][0].Content))
	require.NotNil(t, rec.Pending)
	assert.Equal(t, content, []byte(rec.Pending.Prior.Values[""].Content))

	c := newRecords(t, fs)
	states, err := c.Paths(fs, notesTarget)
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.True(t, states[0].Live, "the migrated claim is the file's content")
	targets, err := c.Targets(project)
	require.NoError(t, err)
	assert.Equal(t, []string{notesTarget}, targets)
	assert.Equal(t, body, read(t, fs, recordPath(notesTarget)), "a read must not write")

	mustCommit(t, c, fs, stage(notesTarget, project, whole(content)))
	assert.Equal(t, content, []byte(read(t, fs, notesTarget)), "restating the migrated claim leaves the file")
	entry := rawEntry(t, fs, notesTarget)
	assert.Equal(t, string(content), entry["content"])
	assert.NotContains(t, entry, "bytes")

	mustCommit(t, c, fs, release(notesTarget, project))
	_, err = fs.Stat(notesTarget)
	assert.True(t, os.IsNotExist(err), "the file ctxloom created leaves with its last claim, as before")
}

// countingFs counts the claims records opened through it.
type countingFs struct {
	afero.Fs
	opened atomic.Int64
}

func (f *countingFs) count(name string) {
	if strings.HasSuffix(name, claimsSuffix) {
		f.opened.Add(1)
	}
}

func (f *countingFs) Open(name string) (afero.File, error) {
	f.count(name)
	return f.Fs.Open(name)
}

func (f *countingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f.count(name)
	return f.Fs.OpenFile(name, flag, perm)
}

// Listing one writer's files, or every writer, reads no claims record: the
// records of other files — every file ever delivered, in every project —
// are not decoded to answer it.
func TestClaimsRecord_TargetsAndWritersDecodeNoUnrelatedRecord(t *testing.T) {
	fs := &countingFs{Fs: afero.NewMemMapFs()}
	c := newRecords(t, fs)
	for i := range 5 {
		mustCommit(t, c, fs, stage("/other/"+strconv.Itoa(i)+".md", other, whole([]byte("theirs\n"))))
	}
	mustCommit(t, c, fs, stage(notesTarget, session, whole([]byte("ours\n"))))
	_, err := c.Targets(session) // the first listing may index records written before the index
	require.NoError(t, err)

	fs.opened.Store(0)
	targets, err := c.Targets(session)
	require.NoError(t, err)
	assert.Equal(t, []string{notesTarget}, targets)
	writers, err := c.Writers()
	require.NoError(t, err)
	assert.Equal(t, []delivery.Writer{session, other}, writers)
	assert.Zero(t, fs.opened.Load(), "claims records opened to list writers and their files")
}

// A store written before the index existed is indexed once, from its records,
// and then answers from the index.
func TestClaimsRecord_RecordsWithoutAnIndexAreIndexedOnce(t *testing.T) {
	fs := &countingFs{Fs: afero.NewMemMapFs()}
	writeClaimsRecord(t, fs, schemaver.Key, claimsKind.Current())
	c := newRecords(t, fs)

	targets, err := c.Targets(project)
	require.NoError(t, err)
	assert.Equal(t, []string{versionedTarget}, targets)
	assert.Positive(t, fs.opened.Load())

	fs.opened.Store(0)
	writers, err := c.Writers()
	require.NoError(t, err)
	assert.Equal(t, []delivery.Writer{project}, writers)
	targets, err = c.Targets(project)
	require.NoError(t, err)
	assert.Equal(t, []string{versionedTarget}, targets)
	assert.Zero(t, fs.opened.Load())
}

// A writer's release takes its file out of its listing, and the last claim's
// release takes the writer out of Writers.
func TestClaimsRecord_ReleaseLeavesTheListings(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := newRecords(t, fs)
	mustCommit(t, c, fs, stage(notesTarget, session, whole([]byte("ours\n"))), stage("/proj/b.md", session, whole([]byte("b\n"))))
	mustCommit(t, c, fs, stage("/proj/b.md", project, whole([]byte("b\n"))))

	mustCommit(t, c, fs, release(notesTarget, session))
	targets, err := c.Targets(session)
	require.NoError(t, err)
	assert.Equal(t, []string{"/proj/b.md"}, targets)

	mustCommit(t, c, fs, release("/proj/b.md", session))
	targets, err = c.Targets(session)
	require.NoError(t, err)
	assert.Empty(t, targets)
	writers, err := c.Writers()
	require.NoError(t, err)
	assert.Equal(t, []delivery.Writer{project}, writers)
}

// A marker a seal left behind — the record no longer names its writer — is
// listed, and the release that listing leads to changes nothing but the
// marker.
func TestClaimsRecord_AStaleMarkerIsReleasedAway(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := newRecords(t, fs)
	mustCommit(t, c, fs, stage(notesTarget, project, whole([]byte("ours\n"))))
	require.NoError(t, c.mark(string(other), notesTarget))
	record := read(t, fs, recordPath(notesTarget))

	targets, err := c.Targets(other)
	require.NoError(t, err)
	assert.Equal(t, []string{notesTarget}, targets)

	mustCommit(t, c, fs, release(notesTarget, other))
	targets, err = c.Targets(other)
	require.NoError(t, err)
	assert.Empty(t, targets)
	assert.Equal(t, "ours\n", read(t, fs, notesTarget))
	assert.Equal(t, record, read(t, fs, recordPath(notesTarget)))
}
