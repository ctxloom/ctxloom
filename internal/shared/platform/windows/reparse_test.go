package windows

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The mount-point reparse buffer is what makes a junction: the NT
// substitute name (\??\ + the absolute target) and the print name, each
// NUL-terminated UTF-16, behind the fixed header.
func TestMountPointReparseData_Layout(t *testing.T) {
	target := `C:\Users\u\.ctxloom\sessions\h\native\claude\projects`
	buf := mountPointReparseData(target)

	subst := utf16.Encode([]rune(`\??\` + target))
	print := utf16.Encode([]rune(target))
	pathBytes := (len(subst) + 1 + len(print) + 1) * 2

	require.Len(t, buf, 8+8+pathBytes)
	le := binary.LittleEndian
	assert.Equal(t, uint32(ioReparseTagMountPoint), le.Uint32(buf[0:]))
	assert.Equal(t, uint16(8+pathBytes), le.Uint16(buf[4:]), "ReparseDataLength counts the four name fields and the path buffer")
	assert.Equal(t, uint16(0), le.Uint16(buf[8:]), "substitute name first")
	assert.Equal(t, uint16(len(subst)*2), le.Uint16(buf[10:]))
	assert.Equal(t, uint16((len(subst)+1)*2), le.Uint16(buf[12:]), "print name after the substitute's NUL")
	assert.Equal(t, uint16(len(print)*2), le.Uint16(buf[14:]))

	gotSubst := make([]uint16, len(subst))
	for i := range gotSubst {
		gotSubst[i] = le.Uint16(buf[16+2*i:])
	}
	assert.Equal(t, `\??\`+target, string(utf16.Decode(gotSubst)))
}
