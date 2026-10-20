package tui

import "io"

// RawLines is w for a terminal that may be in raw mode, where LF moves the
// cursor down without returning the carriage: every LF written through it
// that a CR does not precede gains one, so each line ends CR LF and the next
// starts in column 0. Every line ctxloom writes to the human's terminal while
// it is raw goes through it. Not safe for concurrent use.
func RawLines(w io.Writer) io.Writer { return &rawLines{w: w} }

type rawLines struct {
	w  io.Writer
	cr bool // the last byte written was a CR
}

func (r *rawLines) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p)+8)
	for _, c := range p {
		if c == '\n' && !r.cr {
			out = append(out, '\r')
		}
		out = append(out, c)
		r.cr = c == '\r'
	}
	if _, err := r.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
