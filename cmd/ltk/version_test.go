package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

// runVersion drives the real version command's RunE with a --format value.
// version now reads the root's persistent --format flag (routed through emit
// like cmd/ctxloom), so the test registers that flag on the command and sets
// it, standing in for the inherited root flag.
func runVersion(t *testing.T, format string) (string, error) {
	t.Helper()
	cmd := newVersionCmd()
	cmd.Flags().String("format", "text", "")
	if format != "" {
		if err := cmd.Flags().Set("format", format); err != nil {
			t.Fatalf("set format: %v", err)
		}
	}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := cmd.RunE(cmd, nil)
	return buf.String(), err
}

func TestVersionTextEmitsBareVersion(t *testing.T) {
	out, err := runVersion(t, "text")
	if err != nil {
		t.Fatalf("version text: %v", err)
	}
	if want := Version + "\n"; out != want {
		t.Fatalf("text output = %q, want %q", out, want)
	}
}

func TestVersionJSONEmitsNameAndVersion(t *testing.T) {
	out, err := runVersion(t, "json")
	if err != nil {
		t.Fatalf("version json: %v", err)
	}
	var got cliversion.Info
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if got.Name != progName || got.Version != Version {
		t.Fatalf("json output = %+v, want {Name: %q, Version: %q}", got, progName, Version)
	}
}

// Routing version through clifmt (like cmd/ctxloom) widens it from text/json to
// the full five formats: yaml now renders instead of erroring.
func TestVersionYAMLRenders(t *testing.T) {
	out, err := runVersion(t, "yaml")
	if err != nil {
		t.Fatalf("version yaml: %v", err)
	}
	if !strings.Contains(out, "name: "+progName) || !strings.Contains(out, "version: "+Version) {
		t.Fatalf("yaml output = %q, want name/version keys", out)
	}
}

func TestVersionUnknownFormatErrors(t *testing.T) {
	out, err := runVersion(t, "xml")
	if err == nil {
		t.Fatal("want error for unsupported format")
	}
	if len(out) != 0 {
		t.Fatalf("unsupported format must not write output, got %q", out)
	}
}
