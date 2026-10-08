package clifmt

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type viewName string

type viewInner struct {
	X string `json:"x"`
}

type viewRow struct {
	ID   string        `json:"id"`
	Took time.Duration `json:"took"`
}

type viewOuter struct {
	Name    viewName      `json:"name"`
	Elapsed time.Duration `json:"elapsed"`
	Tags    []string      `json:"tags"`
	Maybe   *string       `json:"maybe,omitempty"`
	Hidden  string        `json:"hidden" clifmt:"-"`
	Skipped string        `json:"-"`
	Inner   viewInner     `json:"inner"`
	PInner  *viewInner    `json:"p_inner"`
	Rows    []viewRow     `json:"rows"`
}

func sampleOuter() viewOuter {
	return viewOuter{
		Name:    "n",
		Elapsed: 90 * time.Second,
		Tags:    []string{"a", "b"},
		Inner:   viewInner{X: "x"},
		PInner:  &viewInner{X: "px"},
		Rows:    []viewRow{{ID: "r1", Took: 2 * time.Second}},
	}
}

func renderWith(t *testing.T, p *Printer, v any, f Format, opts ...Option) string {
	t.Helper()
	var buf bytes.Buffer
	if p == nil {
		p = &Printer{}
	}
	if err := p.Render(&buf, v, f, opts...); err != nil {
		t.Fatalf("Render(%s): %v", f, err)
	}
	return buf.String()
}

func para(s string) func(*ViewCtx, any) (Doc, error) {
	return func(*ViewCtx, any) (Doc, error) { return Doc{Para(s)}, nil }
}

func nameView(s string) Option {
	return ViewFor(func(*ViewCtx, viewName) (Doc, error) { return Doc{Para(s)}, nil })
}

// The most specific layer that applies to a node wins: a call's path view,
// then a call's type view, then the Printer's type view, then derivation.
func TestViewPrecedence(t *testing.T) {
	cases := []struct {
		path, call, printer bool
		want                string
	}{
		{false, false, false, "Name: n\n"},
		{false, false, true, "Name: printer\n"},
		{false, true, false, "Name: call\n"},
		{false, true, true, "Name: call\n"},
		{true, false, false, "Name: path\n"},
		{true, false, true, "Name: path\n"},
		{true, true, false, "Name: path\n"},
		{true, true, true, "Name: path\n"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("path=%v,call=%v,printer=%v", tc.path, tc.call, tc.printer), func(t *testing.T) {
			var popts, copts []Option
			if tc.printer {
				popts = append(popts, nameView("printer"))
			}
			if tc.call {
				copts = append(copts, nameView("call"))
			}
			if tc.path {
				copts = append(copts, At("name", para("path")))
			}
			p, err := New(popts...)
			if err != nil {
				t.Fatal(err)
			}
			got := renderWith(t, p, sampleOuter(), FormatText, copts...)
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("got %q, want it to start %q", got, tc.want)
			}
		})
	}
}

type onlyInner struct {
	Inner viewInner `json:"inner"`
}

// Decorators chain through Derived(): each sees the next lower layer's view.
func TestViewDecoratorsChain(t *testing.T) {
	p, err := New(ViewFor(func(*ViewCtx, viewInner) (Doc, error) {
		return Doc{Section{Title: "Inner", Body: Doc{Para("printer")}}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	decorate := func(label, value string) func(c *ViewCtx) (Doc, error) {
		return func(c *ViewCtx) (Doc, error) {
			d, err := c.Derived()
			return append(d, Field{Label: label, Value: value}), err
		}
	}
	got := renderWith(t, p, onlyInner{}, FormatText,
		ViewFor(func(c *ViewCtx, _ viewInner) (Doc, error) { return decorate("call", "c")(c) }),
		At("inner", func(c *ViewCtx, _ any) (Doc, error) { return decorate("path", "p")(c) }),
	)
	want := "Inner:\n  printer\n\ncall: c\npath: p\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A view that does not call Derived() replaces the node: no lower layer runs.
func TestViewReplaceStopsTheChain(t *testing.T) {
	calls := 0
	lower := ViewFor(func(*ViewCtx, viewInner) (Doc, error) { calls++; return nil, nil })
	p, err := New(ViewFor(func(*ViewCtx, viewInner) (Doc, error) { calls++; return nil, nil }))
	if err != nil {
		t.Fatal(err)
	}
	got := renderWith(t, p, onlyInner{}, FormatText, lower, At("inner", para("replaced")))
	if calls != 0 {
		t.Errorf("%d lower layer(s) ran under a replacing view", calls)
	}
	if got != "Inner: replaced\n" {
		t.Errorf("got %q", got)
	}
}

// Derived() skips a layer for its own node only: the node's children are
// still rendered with every view active.
func TestViewDerivedKeepsChildViews(t *testing.T) {
	got := renderWith(t, nil, onlyInner{}, FormatText,
		At("", func(c *ViewCtx, _ any) (Doc, error) {
			d, err := c.Derived()
			return append(d, Field{Label: "Total", Value: "1"}), err
		}),
		ViewFor(func(*ViewCtx, viewInner) (Doc, error) { return Doc{Para("typed")}, nil }),
	)
	if want := "Inner: typed\nTotal: 1\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A nil Doc hides the node.
func TestViewHidesANode(t *testing.T) {
	got := renderWith(t, nil, sampleOuter(), FormatText, At("name", func(*ViewCtx, any) (Doc, error) { return nil, nil }))
	if strings.Contains(got, "Name:") {
		t.Errorf("hidden field still rendered: %q", got)
	}
	if !strings.Contains(got, "Elapsed:") {
		t.Errorf("siblings lost: %q", got)
	}
}

// Within one scope the same path or type twice is an error, never "last
// wins"; so are options given at the wrong scope or with nothing to match.
func TestOptionConflictsAreErrors(t *testing.T) {
	noop := func(*ViewCtx, any) (Doc, error) { return nil, nil }
	innerView := ViewFor(func(*ViewCtx, viewInner) (Doc, error) { return nil, nil })
	callCases := map[string][]Option{
		"same path twice":         {At("name", noop), At("name", noop)},
		"same type twice":         {innerView, innerView},
		"same writer twice":       {WithWriter(FormatText, func(io.Writer) error { return nil }), WithWriter(FormatText, func(io.Writer) error { return nil })},
		"writer for no format":    {WithWriter(Format("bogus"), func(io.Writer) error { return nil })},
		"nil writer":              {WithWriter(FormatText, nil)},
		"nil path view":           {At("name", nil)},
		"interface type":          {ViewFor(func(*ViewCtx, fmt.Stringer) (Doc, error) { return nil, nil })},
		"pointer type":            {ViewFor(func(*ViewCtx, *viewInner) (Doc, error) { return nil, nil })},
		"nil type view":           {ViewFor[viewInner](nil)},
		"unknown field in a path": {At("nmae", noop)},
	}
	for name, opts := range callCases {
		t.Run("call/"+name, func(t *testing.T) {
			if err := Render(io.Discard, sampleOuter(), FormatText, opts...); err == nil {
				t.Error("Render accepted it")
			}
		})
	}
	printerCases := map[string][]Option{
		"same type twice": {innerView, innerView},
		"path on New":     {At("name", noop)},
		"writer on New":   {WithWriter(FormatJSON, func(io.Writer) error { return nil })},
	}
	for name, opts := range printerCases {
		t.Run("printer/"+name, func(t *testing.T) {
			if _, err := New(opts...); err == nil {
				t.Error("New accepted it")
			}
		})
	}
	t.Run("same type at call and printer scope is layering, not a conflict", func(t *testing.T) {
		p, err := New(innerView)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Render(io.Discard, sampleOuter(), FormatText, innerView); err != nil {
			t.Errorf("Render: %v", err)
		}
	})
}

// A path is checked against the static type before anything renders, in
// every format, so a renamed json tag cannot silently orphan an override.
func TestAtPathsAreValidated(t *testing.T) {
	noop := func(*ViewCtx, any) (Doc, error) { return nil, nil }
	bad := []string{
		"nmae", "inner.y", "name.x", "rows[].nope", "tags[].x", "inner[]",
		"a..b", "rows[]x", "hidden", "skipped", "elapsed.x", ".name",
	}
	for _, p := range bad {
		for _, f := range []Format{FormatText, FormatJSON} {
			err := Render(io.Discard, sampleOuter(), f, At(p, noop))
			if err == nil {
				t.Errorf("At(%q) in %s was accepted", p, f)
				continue
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("At(%q)", p)) {
				t.Errorf("At(%q) error %q does not name the path", p, err)
			}
		}
	}
	good := []string{"", "name", "maybe", "inner.x", "p_inner.x", "rows", "rows[]", "rows[].took", "tags[]"}
	for _, p := range good {
		if err := Render(io.Discard, sampleOuter(), FormatText, At(p, noop)); err != nil {
			t.Errorf("At(%q): %v", p, err)
		}
	}
	if err := Render(io.Discard, []viewRow{}, FormatText, At("[].id", noop)); err != nil {
		t.Errorf("At(\"[].id\") on a top-level list: %v", err)
	}
}

// Views shape text and markdown only; json, yaml and toml always come from
// the json contract.
func TestViewsNeverTouchStructuredOutput(t *testing.T) {
	opts := []Option{
		At("", para("root")),
		At("inner", para("inner")),
		ViewFor(func(*ViewCtx, viewInner) (Doc, error) { return Doc{Para("typed")}, nil }),
	}
	p, err := New(nameView("printer"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Format{FormatJSON, FormatYAML, FormatTOML} {
		plain := renderWith(t, nil, sampleOuter(), f)
		if got := renderWith(t, p, sampleOuter(), f, opts...); got != plain {
			t.Errorf("%s changed under views:\n%s\nwant:\n%s", f, got, plain)
		}
	}
}

// WithWriter may target a structured format, for one call; it pre-empts that
// format only.
func TestWithWriterMayTargetStructuredFormats(t *testing.T) {
	w := WithWriter(FormatJSON, func(w io.Writer) error { _, err := io.WriteString(w, "{\"stream\":1}\n"); return err })
	if got := renderWith(t, nil, sampleOuter(), FormatJSON, w); got != "{\"stream\":1}\n" {
		t.Errorf("json = %q", got)
	}
	if got := renderWith(t, nil, sampleOuter(), FormatText, w, At("name", para("viewed"))); !strings.HasPrefix(got, "Name: viewed\n") {
		t.Errorf("text = %q, want the derived view with its path view", got)
	}
}

// A type view applies wherever its type appears: a field (keeping the
// field's label), a pointer field, a table cell, a list item.
func TestViewForAppliesEverywhereItsTypeAppears(t *testing.T) {
	dur := ViewFor(func(_ *ViewCtx, d time.Duration) (Doc, error) {
		return Doc{Para(fmt.Sprintf("%ds", int(d.Seconds())))}, nil
	})
	inner := ViewFor(func(_ *ViewCtx, v viewInner) (Doc, error) { return Doc{Para("in:" + v.X)}, nil })
	got := renderWith(t, nil, sampleOuter(), FormatText, dur, inner)
	for _, want := range []string{"Elapsed: 90s\n", "Inner: in:x\n", "P Inner: in:px\n", "r1  2s\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("text lacks %q:\n%s", want, got)
		}
	}
	items := renderWith(t, nil, []time.Duration{time.Second, 2 * time.Second}, FormatMarkdown, dur)
	if items != "- 1s\n- 2s\n" {
		t.Errorf("list items = %q", items)
	}
	root := renderWith(t, nil, 3*time.Second, FormatText, dur)
	if root != "3s\n" {
		t.Errorf("root = %q", root)
	}
}

// A view on the elements of a list of structs renders them as blocks, since
// a row of cells cannot hold a view's blocks.
func TestElementViewRendersRowsAsBlocks(t *testing.T) {
	v := struct {
		Rows []viewRow `json:"rows"`
	}{Rows: []viewRow{{ID: "a"}, {ID: "b"}}}
	got := renderWith(t, nil, v, FormatText, At("rows[]", func(c *ViewCtx, v any) (Doc, error) {
		return Doc{Field{Label: c.Label(), Value: v.(viewRow).ID}}, nil
	}))
	if want := "Rows:\n  [1]: a\n  [2]: b\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	derived := renderWith(t, nil, v, FormatText, At("rows[]", func(c *ViewCtx, _ any) (Doc, error) { return c.Derived() }))
	if want := "Rows:\n  [1]:\n    Id: a\n    Took: 0s\n\n  [2]:\n    Id: b\n    Took: 0s\n"; derived != want {
		t.Errorf("derived elements = %q, want %q", derived, want)
	}
}

func TestViewCtxDescribesTheNode(t *testing.T) {
	for _, f := range []Format{FormatText, FormatMarkdown} {
		var seen []string
		record := func(c *ViewCtx, _ any) (Doc, error) {
			seen = append(seen, fmt.Sprintf("%s|%s|%s", c.Format(), c.Path(), c.Label()))
			return c.Derived()
		}
		renderWith(t, nil, sampleOuter(), f, At("", record), At("inner.x", record), At("rows[].took", record))
		want := []string{
			fmt.Sprintf("%s||", f),
			fmt.Sprintf("%s|inner.x|X", f),
			fmt.Sprintf("%s|rows[].took|Took", f),
		}
		if strings.Join(seen, ";") != strings.Join(want, ";") {
			t.Errorf("seen %q, want %q", seen, want)
		}
	}
}

// ViewCtx.View derives another value with the call's type views applied.
func TestViewCtxViewComposes(t *testing.T) {
	got := renderWith(t, nil, sampleOuter(), FormatText,
		At("", func(c *ViewCtx, _ any) (Doc, error) { return c.View(onlyInner{}) }),
		ViewFor(func(*ViewCtx, viewInner) (Doc, error) { return Doc{Para("typed")}, nil }),
	)
	if got != "Inner: typed\n" {
		t.Errorf("got %q", got)
	}
}

func TestViewErrorsNameTheNode(t *testing.T) {
	boom := errors.New("boom")
	err := Render(io.Discard, sampleOuter(), FormatText, At("inner", func(*ViewCtx, any) (Doc, error) { return nil, boom }))
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), `"inner"`) {
		t.Errorf("err = %v, want it to wrap boom and name \"inner\"", err)
	}
	err = Render(io.Discard, sampleOuter(), FormatText, At("rows[].id", func(*ViewCtx, any) (Doc, error) {
		return Doc{Section{Title: "no room"}}, nil
	}))
	if err == nil || !strings.Contains(err.Error(), "rows[].id") {
		t.Errorf("a Section in a table cell: err = %v, want an error naming the cell", err)
	}
}

// Every block lays out in text and markdown; one-line blocks sit together
// and every other neighbour gets a blank line.
func TestDocLayout(t *testing.T) {
	doc := Doc{
		Field{Label: "A", Value: "1"},
		Para("free"),
		Section{Title: "S", Body: Doc{Field{Label: "B", Value: "2"}, Para("two\nlines")}},
		Field{Label: "After", Value: "section"},
		Table{Title: "T", Columns: []string{"c1", "c2"}, Rows: [][]string{{"x", "y|z"}}},
		List{Title: "L", Items: []string{"i1", "i2"}},
		List{Items: []string{"bare"}},
	}
	view := At("", func(*ViewCtx, any) (Doc, error) { return doc, nil })
	text := renderWith(t, nil, struct{}{}, FormatText, view)
	wantText := "A: 1\nfree\n\nS:\n  B: 2\n  two\n  lines\n\nAfter: section\n\nT:\nC1  C2\nx   y|z\n\nL:\n  i1\n  i2\n\nbare\n"
	if text != wantText {
		t.Errorf("text:\n%q\nwant:\n%q", text, wantText)
	}
	md := renderWith(t, nil, struct{}{}, FormatMarkdown, view)
	wantMD := "**A:** 1\nfree\n\n## S\n\n**B:** 2\ntwo\nlines\n\n**After:** section\n\n## T\n\n| c1 | c2 |\n| --- | --- |\n| x | y\\|z |\n\n## L\n\n- i1\n- i2\n\n- bare\n"
	if md != wantMD {
		t.Errorf("markdown:\n%q\nwant:\n%q", md, wantMD)
	}
}

// A view that leaves the root empty still writes something.
func TestEmptyRootViewRendersNone(t *testing.T) {
	got := renderWith(t, nil, sampleOuter(), FormatText, At("", func(*ViewCtx, any) (Doc, error) { return nil, nil }))
	if got != "(none)\n" {
		t.Errorf("got %q", got)
	}
}

// A ViewFor on a list's element type turns its rows into blocks just as an
// At("rows[]") does — whether the view was given to the call or to the
// Printer.
func TestElementTypeViewRendersRowsAsBlocks(t *testing.T) {
	v := struct {
		Rows []viewRow `json:"rows"`
	}{Rows: []viewRow{{ID: "a"}, {ID: "b"}}}
	view := ViewFor(func(c *ViewCtx, r viewRow) (Doc, error) {
		return Doc{Field{Label: c.Label(), Value: r.ID}}, nil
	})
	want := "Rows:\n  [1]: a\n  [2]: b\n"

	if got := renderWith(t, nil, v, FormatText, view); got != want {
		t.Errorf("per-call view: got %q, want %q", got, want)
	}
	p, err := New(view)
	if err != nil {
		t.Fatal(err)
	}
	if got := renderWith(t, p, v, FormatText); got != want {
		t.Errorf("printer view: got %q, want %q", got, want)
	}
}

// A path is resolved past an unexported field: only exported fields carry
// segments, and one that is not exported does not end the search.
func TestAtResolvesAFieldAfterAnUnexportedOne(t *testing.T) {
	v := struct {
		hidden string
		Shown  string `json:"shown"`
	}{hidden: "h", Shown: "s"}
	got := renderWith(t, nil, v, FormatText, At("shown", para("over")))
	if !strings.Contains(got, "over") {
		t.Errorf("the view at shown never fired: %q", got)
	}
}

// A type with several malformed clifmt tags is refused naming the FIRST.
func TestBadHintTagNamesTheFirstOffender(t *testing.T) {
	v := struct {
		A string `json:"a" clifmt:"bogus=1"`
		B string `json:"b" clifmt:"nope=1"`
	}{}
	err := Render(io.Discard, v, FormatText)
	if err == nil {
		t.Fatal("a malformed tag was accepted")
	}
	if !strings.Contains(err.Error(), ".A: bad clifmt tag") || strings.Contains(err.Error(), ".B:") {
		t.Errorf("err = %v; want the first malformed field named", err)
	}
}

type viewEmbedded struct {
	Inner string `json:"inner"`
}

// A nil embedded pointer contributes nothing, and the fields declared after
// it still render.
func TestNilEmbeddedPointerSkipsOnlyItsOwnFields(t *testing.T) {
	v := struct {
		*viewEmbedded
		After string `json:"after"`
	}{After: "kept"}
	got := renderWith(t, nil, v, FormatText)
	if got != "After: kept\n" {
		t.Errorf("got %q", got)
	}
}

// At on a nil pointer hands its func nil, as documented, rather than a value
// it cannot have.
func TestAtOnANilPointerReceivesNil(t *testing.T) {
	v := struct {
		Inner *viewInner `json:"inner"`
	}{}
	var got any = "unset"
	renderWith(t, nil, v, FormatText, At("inner", func(_ *ViewCtx, v any) (Doc, error) {
		got = v
		return nil, nil
	}))
	if got != nil {
		t.Errorf("At(inner) on a nil pointer got %#v, want nil", got)
	}
}
