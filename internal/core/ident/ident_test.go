package ident

import "testing"

func TestRefKey(t *testing.T) {
	tests := []struct {
		ref  Ref
		want string
	}{
		{Ref{Bundle: "code-quality", Kind: KindFragment, Name: "solid"}, "code-quality#fragments/solid"},
		{Ref{Bundle: "tooling", Kind: KindPrompt, Name: "review"}, "tooling#prompts/review"},
		{Ref{Bundle: "tooling", Kind: KindMCP, Name: "postgres"}, "tooling#mcp/postgres"},
	}
	for _, tt := range tests {
		if got := tt.ref.Key(); got != tt.want {
			t.Errorf("Ref%+v.Key() = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestRefCanonicalURL_Local(t *testing.T) {
	r := Ref{IsLocal: true, Bundle: "dev", Kind: KindFragment, Name: "x"}
	if got := r.CanonicalURL(); got != "ctxloom:local" {
		t.Errorf("local Ref.CanonicalURL() = %q, want %q", got, "ctxloom:local")
	}
}
