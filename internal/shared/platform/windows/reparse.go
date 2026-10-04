// Package windows is the platform behaviour of a Windows host. Its system
// calls are compiled only for Windows; the reparse-buffer encoding they write
// is plain data, built everywhere so it is tested everywhere.
package windows

import (
	"encoding/binary"
	"unicode/utf16"
)

// ioReparseTagMountPoint is IO_REPARSE_TAG_MOUNT_POINT: the reparse tag of a
// directory junction.
const ioReparseTagMountPoint = 0xA0000003

// ntPathPrefix is the NT object-namespace prefix a junction's substitute name
// carries before a DOS path.
const ntPathPrefix = `\??\`

// mountPointReparseData is the REPARSE_DATA_BUFFER that makes a directory a
// junction to target (absolute): the 8-byte header (tag, data length,
// reserved), the four name offsets/lengths, then the substitute name
// (\??\target) and the print name (target), each NUL-terminated UTF-16LE.
// The lengths exclude the NULs; the data length counts everything after the
// header.
func mountPointReparseData(target string) []byte {
	subst := utf16.Encode([]rune(ntPathPrefix + target))
	print := utf16.Encode([]rune(target))
	pathBytes := (len(subst) + 1 + len(print) + 1) * 2
	buf := make([]byte, 16+pathBytes)
	le := binary.LittleEndian
	le.PutUint32(buf[0:], ioReparseTagMountPoint)
	le.PutUint16(buf[4:], uint16(8+pathBytes))
	le.PutUint16(buf[8:], 0)
	le.PutUint16(buf[10:], uint16(len(subst)*2))
	le.PutUint16(buf[12:], uint16((len(subst)+1)*2))
	le.PutUint16(buf[14:], uint16(len(print)*2))
	off := 16
	for _, u := range subst {
		le.PutUint16(buf[off:], u)
		off += 2
	}
	off += 2
	for _, u := range print {
		le.PutUint16(buf[off:], u)
		off += 2
	}
	return buf
}
