package clidiag

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

const testRemedy = "ctxloom deps pull"

func TestWarnRemedy_TextAppendsFixLine(t *testing.T) {
	resetStructured(t)
	SetStructured(false)
	var buf bytes.Buffer
	restore := SetSink(&buf)
	defer restore()

	WarnRemedy("ctxloom", testRemedy, "sync %s failed", "x")
	want := "ctxloom: warning: sync x failed" + clifmt.FixLine("  ", testRemedy) + "\n"
	if buf.String() != want {
		t.Fatalf("WarnRemedy = %q; want %q", buf.String(), want)
	}
}

func TestWarnRemedy_StructuredCarriesRemedy(t *testing.T) {
	resetStructured(t)
	SetStructured(true)
	var buf bytes.Buffer
	restore := SetSink(&buf)
	defer restore()

	WarnRemedy("ctxloom", testRemedy, "sync failed")
	var env clifmt.WarningEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, buf.String())
	}
	if env.Remedy != testRemedy || env.Warning != "sync failed" {
		t.Fatalf("envelope = %+v; want remedy %q", env, testRemedy)
	}
}

func TestWarnRemedyOnce_DedupsAndCarriesRemedy(t *testing.T) {
	resetStructured(t)
	SetStructured(false)
	ResetWarnOnce()
	t.Cleanup(ResetWarnOnce)
	var buf bytes.Buffer
	restore := SetSink(&buf)
	defer restore()

	WarnRemedyOnce("ctxloom", testRemedy, "once %d", 1)
	WarnRemedyOnce("ctxloom", testRemedy, "once %d", 1)
	if n := strings.Count(buf.String(), "fix: "+testRemedy); n != 1 {
		t.Fatalf("got %d fix lines, want 1: %q", n, buf.String())
	}
}

func TestWarn_NoRemedyNoFixLine(t *testing.T) {
	resetStructured(t)
	SetStructured(false)
	var buf bytes.Buffer
	restore := SetSink(&buf)
	defer restore()
	Warn("ctxloom", "plain")
	if strings.Contains(buf.String(), "fix:") {
		t.Fatalf("advisory grew a fix line: %q", buf.String())
	}
}
