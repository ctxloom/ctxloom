package cliversion

import (
	"strings"
	"testing"
)

func TestRenderText(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, Info{Name: "ctxloom", Version: "v1.2.3"}, "text"); err != nil {
		t.Fatalf("Render text: %v", err)
	}
	if got := b.String(); got != "v1.2.3\n" {
		t.Fatalf("text output = %q, want %q", got, "v1.2.3\n")
	}
}

func TestRenderDefaultIsText(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, Info{Name: "ctxloom", Version: "dev"}, ""); err != nil {
		t.Fatalf("Render empty format: %v", err)
	}
	if got := b.String(); got != "dev\n" {
		t.Fatalf("default output = %q, want %q", got, "dev\n")
	}
}

func TestRenderJSON(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, Info{Name: "ltk", Version: "v0.0.4"}, "json"); err != nil {
		t.Fatalf("Render json: %v", err)
	}
	got := b.String()
	for _, want := range []string{`"name": "ltk"`, `"version": "v0.0.4"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("json output %q missing %q", got, want)
		}
	}
}

func TestRenderUnknownFormat(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, Info{Name: "x", Version: "y"}, "yaml"); err == nil {
		t.Fatal("expected error for unknown format, got nil")
	}
}
