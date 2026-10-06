package testenv

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// ScreenText is the text a terminal shows once it has interpreted stream:
// one line per row, top to bottom, trailing blanks trimmed.
//
// A cell-diffing renderer (bubbletea v2's) redraws only the cells that
// changed, moving the cursor over the rest, so text a user plainly sees need
// never appear contiguously in the bytes. A wait on what the user sees reads
// this, not the raw stream.
//
// Rows and columns count from the cursor's position where stream begins, and
// rows extend without bound in both directions: an inline renderer moves only
// relative to where it started. Cells stream never wrote read as blank.
func ScreenText(stream string) string {
	s := screen{rows: map[int][]string{}}
	p := ansi.NewParser()
	var state byte
	for len(stream) > 0 {
		seq, width, n, next := ansi.DecodeSequence(stream, state, p)
		state, stream = next, stream[n:]
		switch {
		case width > 0:
			s.print(seq, width)
		case ansi.HasCsiPrefix(seq):
			s.csi(ansi.Cmd(p.Command()).Final(), p.Params())
		case len(seq) == 1:
			s.control(seq[0])
		}
	}
	return s.String()
}

// screen is a grid of cells keyed by row. A cell holds the grapheme drawn in
// it; a wide grapheme's trailing cells hold "".
type screen struct {
	rows     map[int][]string
	row, col int
	last     string
}

func (s *screen) line() []string {
	r := s.rows[s.row]
	for len(r) < s.col {
		r = append(r, " ")
	}
	return r
}

func (s *screen) print(g string, width int) {
	r := s.line()
	for len(r) < s.col+width {
		r = append(r, " ")
	}
	r[s.col] = g
	for i := 1; i < width; i++ {
		r[s.col+i] = ""
	}
	s.rows[s.row], s.col, s.last = r, s.col+width, g
}

func (s *screen) control(c byte) {
	switch c {
	case '\r':
		s.col = 0
	case '\n':
		s.row++
	case '\b':
		s.col = max(s.col-1, 0)
	case '\t':
		s.col = (s.col/8 + 1) * 8
	}
}

// csiOps are the cursor and editing sequences a renderer draws with; any
// other CSI (SGR, modes) changes no cell text.
var csiOps = map[byte]func(s *screen, n int){
	'A': func(s *screen, n int) { s.row -= n },
	'B': func(s *screen, n int) { s.row += n },
	'C': func(s *screen, n int) { s.col += n },
	'D': func(s *screen, n int) { s.col = max(s.col-n, 0) },
	'E': func(s *screen, n int) { s.row, s.col = s.row+n, 0 },
	'F': func(s *screen, n int) { s.row, s.col = s.row-n, 0 },
	'G': func(s *screen, n int) { s.col = n - 1 },
	'P': func(s *screen, n int) { s.edit(func(r []string) []string { return slices.Delete(r, s.col, min(s.col+n, len(r))) }) },
	'@': func(s *screen, n int) { s.edit(func(r []string) []string { return slices.Insert(r, s.col, blanks(n)...) }) },
	'X': func(s *screen, n int) { s.edit(func(r []string) []string { return blankFrom(r, s.col, n) }) },
	'b': func(s *screen, n int) {
		for range n {
			s.print(s.last, ansi.StringWidth(s.last))
		}
	},
}

func (s *screen) csi(final byte, params ansi.Params) {
	if final == 'K' {
		s.eraseLine(params)
		return
	}
	if op, ok := csiOps[final]; ok {
		n, _, _ := params.Param(0, 1)
		op(s, max(n, 1))
	}
}

// eraseLine is EL: 0 erases from the cursor to the line's end, 1 from its
// start through the cursor, 2 the whole line.
func (s *screen) eraseLine(params ansi.Params) {
	mode, _, _ := params.Param(0, 0)
	s.edit(func(r []string) []string {
		switch mode {
		case 0:
			return r[:min(s.col, len(r))]
		case 1:
			return blankFrom(r, 0, s.col+1)
		}
		return nil
	})
}

func (s *screen) edit(f func([]string) []string) { s.rows[s.row] = f(s.line()) }

func blanks(n int) []string {
	b := make([]string, n)
	for i := range b {
		b[i] = " "
	}
	return b
}

func blankFrom(r []string, from, n int) []string {
	for i := from; i < min(from+n, len(r)); i++ {
		r[i] = " "
	}
	return r
}

func (s *screen) String() string {
	if len(s.rows) == 0 {
		return ""
	}
	keys := slices.Sorted(func(yield func(int) bool) {
		for k := range s.rows {
			if !yield(k) {
				return
			}
		}
	})
	var b strings.Builder
	for row := keys[0]; row <= keys[len(keys)-1]; row++ {
		b.WriteString(strings.TrimRight(strings.Join(s.rows[row], ""), " "))
		b.WriteByte('\n')
	}
	return b.String()
}
