package testenv

import (
	"strings"
	"testing"
)

// overlayPauseRedraw is the overlay's output, verbatim from a CI run, after
// its pause key: the pending hint "pausing <harp>…" was painted before the
// coordinator answered, so the renderer drew the confirmation as a cell diff
// against it — it skipped the five cells " paus" the two lines share and
// wrote only "ed <harp>". The bytes never hold "paused <harp>"; the screen
// does.
const overlayPauseRedraw = "\x1b[8A\x1b[34Crural-stiff-fifth (fixe\x1b[12P\r\n ● nifty-lusty-fiber·nift…│          with only\r\n   ◐ rural-stiff-fifth·f… │          a stderr warning, discarding the runtime and permissions you as\r\r\n\x1b[37Cked for —\r\n\x1b[37Cconfirm the name resolves before trusting the isolation you req\r\r\n\x1b[37Cuested.\r\n\r\n\x1b[37Cgo\r\n\x1b[Cpausing rural-stiff-fifth…  ─ j/k move · enter feed · a approvals · i inject · ? ask · s summarize\r\x1b[5Ced rural-stiff-fifth  ─ j/k move · enter feed · a approvals · i inject · ? ask · s summarize ·\r\x1b[8A\x1b[67Cpaused by the human · live · ▼ f…\r\r\n\r\n\x1b[3C‖"

func TestScreenText_ShowsACellDiffedRedraw(t *testing.T) {
	const confirmed = "paused rural-stiff-fifth"
	if strings.Contains(overlayPauseRedraw, confirmed) {
		t.Fatalf("fixture no longer exercises a cell-diffed redraw: the bytes hold %q", confirmed)
	}
	screen := ScreenText(overlayPauseRedraw)
	if !strings.Contains(screen, " "+confirmed+"  ─ j/k move") {
		t.Fatalf("screen does not show %q on the hint bar:\n%s", confirmed, screen)
	}
	if strings.Contains(screen, "pausing rural-stiff-fifth") {
		t.Fatalf("screen still shows the pending hint the redraw overwrote:\n%s", screen)
	}
}

func TestScreenText_InterpretsEachRendererOp(t *testing.T) {
	cases := []struct{ name, stream, want string }{
		{"print and newline", "ab\r\ncd", "ab\ncd\n"},
		{"cursor up from origin", "a\r\n\x1b[Ab", "b\n"},
		{"cursor down", "a\x1b[2Bb", "a\n\n b\n"},
		{"cursor forward over kept cells", "abcd\r\x1b[2CX", "abXd\n"},
		{"cursor back", "abc\x1b[2DX", "aXc\n"},
		{"next line", "ab\x1b[1EX", "ab\nX\n"},
		{"previous line", "\r\nab\x1b[FX", "X\nab\n"},
		{"column absolute", "abcd\x1b[2GX", "aXcd\n"},
		{"delete chars", "abcdef\r\x1b[C\x1b[2P", "adef\n"},
		{"insert blanks", "abc\r\x1b[C\x1b[2@", "a  bc\n"},
		{"erase chars", "abcdef\r\x1b[C\x1b[2X", "a  def\n"},
		{"repeat last", "a\x1b[3b", "aaaa\n"},
		{"erase to line end", "abcdef\r\x1b[2C\x1b[K", "ab\n"},
		{"erase to line start", "abcdef\r\x1b[2C\x1b[1K", "   def\n"},
		{"erase line", "abcdef\x1b[2K", "\n"},
		{"backspace and tab", "ab\bX\tY", "aX      Y\n"},
		{"sgr changes no text", "a\x1b[31mb\x1b[0m", "ab\n"},
		{"wide grapheme", "日x\r\x1b[2CY", "日Y\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ScreenText(c.stream); got != c.want {
				t.Fatalf("ScreenText(%q) = %q, want %q", c.stream, got, c.want)
			}
		})
	}
}
