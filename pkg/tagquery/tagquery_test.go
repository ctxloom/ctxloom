package tagquery_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/pkg/tagquery"
)

func TestParseShapes(t *testing.T) {
	tests := []struct {
		name string
		path string
		want tagquery.Expr
	}{
		{
			name: "single tag",
			path: "/foo",
			want: tagquery.Term("foo"),
		},
		{
			name: "explicit and",
			path: "/foo/bar/and",
			want: tagquery.And(tagquery.Term("foo"), tagquery.Term("bar")),
		},
		{
			name: "not",
			path: "/foo/not",
			want: tagquery.Not(tagquery.Term("foo")),
		},
		{
			name: "or of and",
			path: "/a/b/and/c/or",
			want: tagquery.Or(
				tagquery.And(tagquery.Term("a"), tagquery.Term("b")),
				tagquery.Term("c"),
			),
		},
		{
			name: "implicit and (two bare terms)",
			path: "/foo/bar",
			want: tagquery.And(tagquery.Term("foo"), tagquery.Term("bar")),
		},
		{
			name: "implicit and (three bare terms, left-associated)",
			path: "/foo/bar/baz",
			want: tagquery.And(
				tagquery.And(tagquery.Term("foo"), tagquery.Term("bar")),
				tagquery.Term("baz"),
			),
		},
		{
			name: "operators are case-insensitive",
			path: "/foo/bar/AND",
			want: tagquery.And(tagquery.Term("foo"), tagquery.Term("bar")),
		},
		{
			name: "tags are case-sensitive (distinct from operator casing)",
			path: "/Foo/Bar/And",
			want: tagquery.And(tagquery.Term("Foo"), tagquery.Term("Bar")),
		},
		{
			name: "empty path is Universal",
			path: "",
			want: tagquery.Universal(),
		},
		{
			name: "single slash is Universal",
			path: "/",
			want: tagquery.Universal(),
		},
		{
			name: "double slash is Universal",
			path: "//",
			want: tagquery.Universal(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tagquery.Parse(tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestImplicitAndMatchesExplicitAnd(t *testing.T) {
	implicit, err := tagquery.Parse("/foo/bar")
	require.NoError(t, err)
	explicit, err := tagquery.Parse("/foo/bar/and")
	require.NoError(t, err)

	assert.Equal(t, explicit, implicit)

	has := func(tags ...string) func(string) bool {
		set := make(map[string]struct{}, len(tags))
		for _, t := range tags {
			set[t] = struct{}{}
		}
		return func(tag string) bool {
			_, ok := set[tag]
			return ok
		}
	}

	assert.Equal(t, explicit.Matches(has("foo", "bar")), implicit.Matches(has("foo", "bar")))
	assert.Equal(t, explicit.Matches(has("foo")), implicit.Matches(has("foo")))
}

func TestMatches(t *testing.T) {
	has := func(tag string) bool {
		return tag == "foo" || tag == "bar"
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"and both present", "/foo/bar/and", true},
		{"and one missing", "/foo/baz/and", false},
		{"or one present", "/foo/baz/or", true},
		{"or none present", "/qux/baz/or", false},
		{"not on present tag", "/foo/not", false},
		{"not on absent tag", "/baz/not", true},
		{"empty query is universal, always matches", "", true},
		{"single slash is universal, always matches", "/", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := tagquery.Parse(tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, expr.Matches(has))
		})
	}
}

func TestEmptyExprAlwaysFalse(t *testing.T) {
	// Empty is not reachable via Parse but is part of the public AST; it
	// must always evaluate to false regardless of predicate.
	e := tagquery.Empty()
	assert.False(t, e.Matches(func(string) bool { return true }))
	assert.False(t, e.Matches(func(string) bool { return false }))
}

func TestUniversalAlwaysTrue(t *testing.T) {
	u := tagquery.Universal()
	assert.True(t, u.Matches(func(string) bool { return true }))
	assert.True(t, u.Matches(func(string) bool { return false }))
}

func TestParseArityErrors(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantOp  string
		wantGot int
	}{
		{"and with zero operands", "/and", "and", 0},
		{"or with zero operands", "/or", "or", 0},
		{"and with one operand", "/foo/and", "and", 1},
		{"or with one operand", "/foo/or", "or", 1},
		{"not with zero operands", "/not", "not", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tagquery.Parse(tc.path)
			require.Error(t, err)

			var perr *tagquery.ParseError
			require.True(t, errors.As(err, &perr), "expected *tagquery.ParseError, got %T: %v", err, err)
			assert.Equal(t, tc.wantOp, perr.Op)
			assert.Equal(t, tc.wantGot, perr.Got)
		})
	}
}

func TestParseDisableOr(t *testing.T) {
	// Enabled by default.
	_, err := tagquery.Parse("/foo/bar/or")
	require.NoError(t, err)

	// Disabled via option.
	_, err = tagquery.Parse("/foo/bar/or", tagquery.DisableOr())
	require.Error(t, err)
	assert.ErrorIs(t, err, tagquery.ErrOrDisabled)

	// A query that never uses `or` is unaffected by the option.
	expr, err := tagquery.Parse("/foo/bar/and", tagquery.DisableOr())
	require.NoError(t, err)
	assert.Equal(t, tagquery.And(tagquery.Term("foo"), tagquery.Term("bar")), expr)
}

func TestExtractTags(t *testing.T) {
	expr, err := tagquery.Parse("/a/b/and/c/or/a/not")
	require.NoError(t, err)

	// Query is (implicit and of) Or(And(a,b), c) and Not(a).
	got := expr.ExtractTags()
	want := map[string]struct{}{
		"a": {},
		"b": {},
		"c": {},
	}
	assert.Equal(t, want, got)
}

func TestExtractTagsEmptyForUniversalAndEmpty(t *testing.T) {
	assert.Empty(t, tagquery.Universal().ExtractTags())
	assert.Empty(t, tagquery.Empty().ExtractTags())
}

func TestStringRoundTrips(t *testing.T) {
	tests := []string{
		"foo",
		"foo/bar/and",
		"foo/not",
		"a/b/and/c/or",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			expr, err := tagquery.Parse(path)
			require.NoError(t, err)

			rendered := expr.String()
			assert.Equal(t, path, rendered)

			reparsed, err := tagquery.Parse(rendered)
			require.NoError(t, err)
			assert.Equal(t, expr, reparsed)
		})
	}
}

func TestStringUniversalIsEmpty(t *testing.T) {
	assert.Equal(t, "", tagquery.Universal().String())
	assert.Equal(t, "", tagquery.Empty().String())
}

func TestValidateTagRejectsSlash(t *testing.T) {
	err := tagquery.ValidateTag("foo/bar")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "foo/bar")
	assert.Contains(t, err.Error(), "/")
}

func TestValidateTagRejectsReservedWords(t *testing.T) {
	for _, tag := range []string{"and", "or", "not", "AND", "Or", "NOT", " and ", "  not"} {
		t.Run(tag, func(t *testing.T) {
			err := tagquery.ValidateTag(tag)
			require.Error(t, err, "tag %q should be rejected as a reserved operator word", tag)
		})
	}
}

func TestValidateTagAcceptsOrdinaryTags(t *testing.T) {
	for _, tag := range []string{"urgent", "release", "android", "cannot", "note", "north"} {
		t.Run(tag, func(t *testing.T) {
			assert.NoError(t, tagquery.ValidateTag(tag))
		})
	}
}

func TestValidateTagEmptyDoesNotPanicAndIsNotRejected(t *testing.T) {
	assert.NotPanics(t, func() {
		err := tagquery.ValidateTag("")
		assert.NoError(t, err)
	})
	assert.NoError(t, tagquery.ValidateTag("   "))
}

func TestValidateTagsReturnsFirstError(t *testing.T) {
	require.NoError(t, tagquery.ValidateTags([]string{"urgent", "release"}))

	err := tagquery.ValidateTags([]string{"urgent", "foo/bar", "and"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "foo/bar")
}
