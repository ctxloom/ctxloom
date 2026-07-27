package testenv

import "testing"

// TestParseNamedArray_MissingArrayIsAnError pins U163-F05: listNames
// (and ListToolDetails, the same shape) returned (nil, nil) when the named
// result array was missing or not an array — so a server advertising ZERO
// tools/resources was indistinguishable from one whose response was
// malformed. TestCompleteness's "mcp tools"/"mcp resources" subtests then
// iterate an empty slice and pass vacuously: the suite's only completeness
// gate could report green against a server that registered nothing at all.
func TestParseNamedArray_MissingArrayIsAnError(t *testing.T) {
	result := map[string]any{"notTools": []any{}}
	_, err := parseNamedArray(result, "tools", "name")
	if err == nil {
		t.Fatal("expected an error when the named array key is absent, got nil")
	}
}

// TestParseNamedArray_WrongTypeIsAnError covers the "present but not an
// array" malformation (e.g. a server that sends tools: null or tools: {}).
func TestParseNamedArray_WrongTypeIsAnError(t *testing.T) {
	result := map[string]any{"tools": "not an array"}
	_, err := parseNamedArray(result, "tools", "name")
	if err == nil {
		t.Fatal("expected an error when the named key is not an array, got nil")
	}
}

// TestParseNamedArray_EmptyArrayIsNotAnError is the legitimate "zero items"
// case: an array that IS present and IS empty is a real, valid answer, not
// a malformation.
func TestParseNamedArray_EmptyArrayIsNotAnError(t *testing.T) {
	result := map[string]any{"tools": []any{}}
	names, err := parseNamedArray(result, "tools", "name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("names = %v, want empty", names)
	}
}

// TestParseNamedArray_ExtractsNamedField is the ordinary success path.
func TestParseNamedArray_ExtractsNamedField(t *testing.T) {
	result := map[string]any{"tools": []any{
		map[string]any{"name": "foo"},
		map[string]any{"name": "bar"},
	}}
	names, err := parseNamedArray(result, "tools", "name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 || names[0] != "foo" || names[1] != "bar" {
		t.Fatalf("names = %v, want [foo bar]", names)
	}
}
