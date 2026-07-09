package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

// runVersion drives the real version command with the given --format and
// returns its stdout. It exercises ltk's wiring (name=progName) on top of the
// shared cliversion.Render contract.
func runVersion(t *testing.T, format string) (string, error) {
	t.Helper()
	cmd := newVersionCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Flags().Set("format", format); err != nil {
		t.Fatalf("set format: %v", err)
	}
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

func TestVersionUnknownFormatErrors(t *testing.T) {
	out, err := runVersion(t, "yaml")
	if err == nil {
		t.Fatal("want error for unknown format")
	}
	if len(out) != 0 {
		t.Fatalf("unknown format must not write output, got %q", out)
	}
}
