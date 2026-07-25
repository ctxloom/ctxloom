// Package cmd implements the Windows cmd.exe (batch) frontend. No AST parser
// exists for cmd, so this is a small hand-written lexer + parser covering the
// command grammar that matters for rule matching:
//
//   - operators: & (sequence), && (and), || (or), | (pipe)
//   - grouping: ( … ) (flattened — its commands are surfaced for matching)
//   - quoting: "…" (one token; quotes stripped)
//   - escaping: ^ (the next character is literal, incl. & | ( ) etc.)
//   - variables: %VAR% is parsed as part of the word (not expanded — cmd value
//     resolution is out of scope; see shell for variable resolution)
//   - redirections: >, >>, <, 2>, 2>&1 (consumed, kept off argv)
//
// Control-flow keywords (for/if/do/else) are surfaced as ordinary command words
// rather than interpreted; this is sufficient for matching real programs and is
// documented as a limitation.
package cmd

import (
	"context"
	"strings"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// Frontend lowers cmd.exe command lines into the IR.
type Frontend struct{}

// New returns a cmd Frontend.
func New() *Frontend { return &Frontend{} }

// Shells reports the dialects handled by this frontend.
func (f *Frontend) Shells() []ir.Shell { return []ir.Shell{ir.ShellCmd} }

// Parse lowers src into a Script. It is best-effort and never errors.
func (f *Frontend) Parse(_ context.Context, shell ir.Shell, src string) (*ir.Script, error) {
	l := &lexer{s: src}
	p := &parser{toks: l.run()}
	pipelines := p.parseSequence()
	return &ir.Script{Shell: shell, Pipelines: pipelines}, nil
}

// --- lexer ---

type tkind uint8

const (
	tWord tkind = iota
	tAnd        // &&
	tOr         // ||
	tSeq        // & or newline
	tPipe       // |
	tLParen
	tRParen
	tRedir // text holds the operator
)

type tok struct {
	kind tkind
	text string
}

type lexer struct {
	s       string
	i       int
	toks    []tok
	buf     strings.Builder
	hasWord bool
}

func (l *lexer) flush() {
	if l.hasWord {
		l.toks = append(l.toks, tok{kind: tWord, text: l.buf.String()})
		l.buf.Reset()
		l.hasWord = false
	}
}

func (l *lexer) add(b byte)             { l.buf.WriteByte(b); l.hasWord = true }
func (l *lexer) emit(k tkind, t string) { l.toks = append(l.toks, tok{kind: k, text: t}) }

func (l *lexer) run() []tok {
	for l.i < len(l.s) {
		switch l.s[l.i] {
		case '^':
			l.lexCaret()
		case '"':
			l.lexQuoted()
		case ' ', '\t', '\r':
			l.flush()
			l.i++
		case '\n':
			l.flush()
			l.emit(tSeq, "\n")
			l.i++
		case '&':
			l.lexPair('&', tAnd, "&&", tSeq, "&")
		case '|':
			l.lexPair('|', tOr, "||", tPipe, "|")
		case '(':
			l.flush()
			l.emit(tLParen, "(")
			l.i++
		case ')':
			l.flush()
			l.emit(tRParen, ")")
			l.i++
		case '>', '<':
			l.lexRedir()
		case '%':
			if !l.lexPercent() {
				l.add(l.s[l.i])
				l.i++
			}
		default:
			l.add(l.s[l.i])
			l.i++
		}
	}
	l.flush()
	return l.toks
}

// lexCaret handles ^: the next character is taken literally.
func (l *lexer) lexCaret() {
	if l.i+1 < len(l.s) {
		l.add(l.s[l.i+1])
		l.i += 2
	} else {
		l.i++
	}
}

// lexQuoted reads a "…" segment (quotes stripped) until the next quote or end.
func (l *lexer) lexQuoted() {
	l.hasWord = true
	l.i++
	for l.i < len(l.s) && l.s[l.i] != '"' {
		if l.s[l.i] == '%' && l.lexPercent() {
			continue
		}
		l.buf.WriteByte(l.s[l.i])
		l.i++
	}
	if l.i < len(l.s) {
		l.i++ // closing quote
	}
}

// lexPercent consumes a %VAR% run into the current word (kept literally; cmd
// value resolution is out of scope). Returns false if there is no closing %.
func (l *lexer) lexPercent() bool {
	j := strings.IndexByte(l.s[l.i+1:], '%')
	if j < 0 {
		return false
	}
	l.buf.WriteString(l.s[l.i : l.i+j+2])
	l.hasWord = true
	l.i += j + 2
	return true
}

// lexPair emits a doubled operator (e.g. &&) or its single form (&).
func (l *lexer) lexPair(ch byte, dbl tkind, dblText string, single tkind, singleText string) {
	l.flush()
	if l.i+1 < len(l.s) && l.s[l.i+1] == ch {
		l.emit(dbl, dblText)
		l.i += 2
	} else {
		l.emit(single, singleText)
		l.i++
	}
}

// lexRedir reads a redirection operator, dropping any file-descriptor prefix
// (e.g. the 2 in 2>) so it does not leak into argv.
func (l *lexer) lexRedir() {
	if l.hasWord && isDigits(l.buf.String()) {
		l.buf.Reset()
		l.hasWord = false
	} else {
		l.flush()
	}
	c := l.s[l.i]
	o := string(c)
	l.i++
	if c == '>' && l.i < len(l.s) && l.s[l.i] == '>' {
		o += ">"
		l.i++
	}
	if l.i < len(l.s) && l.s[l.i] == '&' {
		o += "&"
		l.i++
	}
	l.emit(tRedir, o)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// --- parser ---

type parser struct {
	toks []tok
	pos  int
}

func (p *parser) peek() (tok, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return tok{}, false
}

// parseSequence parses statements separated by & && || newline, until EOF or a
// ')'. The ')' is left for the caller (parsePipeline) to consume.
func (p *parser) parseSequence() []ir.Pipeline {
	var out []ir.Pipeline
	conn := ir.ConnNone
	for {
		t, ok := p.peek()
		if !ok || t.kind == tRParen {
			break
		}
		out = append(out, p.parsePipeline(conn)...)
		t, ok = p.peek()
		if !ok || t.kind == tRParen {
			break
		}
		switch t.kind {
		case tAnd:
			conn = ir.ConnAnd
		case tOr:
			conn = ir.ConnOr
		default:
			conn = ir.ConnSeq
		}
		p.pos++ // consume the separator
	}
	return out
}

// parsePipeline parses one pipeline (commands joined by |) or a ( … ) group,
// which is flattened into its inner pipelines.
func (p *parser) parsePipeline(conn ir.Connector) []ir.Pipeline {
	if t, ok := p.peek(); ok && t.kind == tLParen {
		p.pos++ // (
		inner := p.parseSequence()
		if t2, ok := p.peek(); ok && t2.kind == tRParen {
			p.pos++
		}
		p.skipRedirs() // trailing redirs on the group, e.g. (...) > file
		return inner
	}

	var cmds []ir.SimpleCommand
	for {
		if sc, ok := p.parseSimpleCommand(); ok {
			cmds = append(cmds, sc)
		}
		if t, ok := p.peek(); ok && t.kind == tPipe {
			p.pos++
			continue
		}
		break
	}
	if len(cmds) == 0 {
		return nil
	}
	return []ir.Pipeline{{Connector: conn, Commands: cmds}}
}

func (p *parser) parseSimpleCommand() (ir.SimpleCommand, bool) {
	var sc ir.SimpleCommand
	got := false
	for {
		t, ok := p.peek()
		if !ok {
			break
		}
		switch t.kind {
		case tWord:
			sc.Argv = append(sc.Argv, t.text)
			got = true
			p.pos++
		case tRedir:
			p.pos++
			target := ""
			if w, ok := p.peek(); ok && w.kind == tWord {
				target = w.text
				p.pos++
			}
			sc.Redirects = append(sc.Redirects, ir.Redirect{Op: t.text, Target: target})
		default:
			return sc, got
		}
	}
	return sc, got
}

// skipRedirs consumes redirection operators (and their targets) with no command.
func (p *parser) skipRedirs() {
	for {
		t, ok := p.peek()
		if !ok || t.kind != tRedir {
			return
		}
		p.pos++
		if w, ok := p.peek(); ok && w.kind == tWord {
			p.pos++ // target
		}
	}
}
