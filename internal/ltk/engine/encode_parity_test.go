package engine

import "testing"

// Every Adapter's Encode obeys ONE decision contract; only the wire bytes of a
// denial are engine-specific. This pins that contract at the public seam
// (Adapter.Encode) across every registered engine, so it is unchanged by how
// the adapters are implemented behind it (U067-F07).
//
// The four arms are the whole space Response admits, and each carries a
// distinct obligation:
//
//   - clean allow      → a ZERO Output: the host's own permission flow proceeds.
//   - unanalyzed allow → still an allow on the wire, plus a stderr diagnostic,
//     so "allowed per on_parse_error" is not byte-identical to "checked and
//     nothing matched" (U005-F01/U067-F03).
//   - deny             → a non-empty decision document on STDOUT at exit 0.
//     Never an exit code: both hosts fail OPEN on a non-zero hook, so a denial
//     signalled that way is invisible and the action proceeds.
//   - reasonless deny  → still a non-empty document, because a deny that renders
//     to nothing tells the agent neither why nor how to comply (U067-F01).
func TestEncodeParityAcrossAdapters(t *testing.T) {
	cases := []struct {
		name         string
		resp         Response
		wantStdout   bool
		wantStderr   bool
		wantExitCode int
	}{
		{"clean allow", Response{Allow: true}, false, false, 0},
		{"unanalyzed allow", unanalyzedResponse(), false, true, 0},
		{"deny", Response{Reason: "no rm -rf", Suggest: "use trash"}, true, false, 0},
		{"reasonless deny", Response{}, true, false, 0},
	}

	for _, e := range All() {
		a, ok := e.(Adapter)
		if !ok {
			t.Fatalf("%T does not implement Adapter", e)
		}
		for _, c := range cases {
			t.Run(a.Name()+"/"+c.name, func(t *testing.T) {
				out, err := a.Encode(c.resp)
				if err != nil {
					t.Fatalf("Encode: %v", err)
				}
				if got := len(out.Stdout) > 0; got != c.wantStdout {
					t.Errorf("stdout bytes = %v, want %v", got, c.wantStdout)
				}
				if got := len(out.Stderr) > 0; got != c.wantStderr {
					t.Errorf("stderr bytes = %v, want %v", got, c.wantStderr)
				}
				if out.ExitCode != c.wantExitCode {
					t.Errorf("exit code = %d, want %d", out.ExitCode, c.wantExitCode)
				}
			})
		}
	}
}

// The management half has the same shape: Install and Uninstall are VERBATIM
// identical across the two engines today (both delegate to the shared
// merge/remove helpers), differing only in the matcher each installs. A parity
// test cannot be red on identical bodies, so its job here is to pin the SHARED
// behaviour — install is idempotent, uninstall is its exact inverse, and each
// engine's matcher is derived from its OWN gated-tool list rather than the
// other's.
func TestManagementParityAcrossEngines(t *testing.T) {
	const cmdLine = "ltk evaluate --config .ltk/config.yaml"

	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			installed, note, err := e.Install(nil, cmdLine)
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			if len(installed) == 0 {
				t.Fatal("Install produced zero bytes")
			}
			if note != "" {
				t.Errorf("a fresh install has nothing to repair, got note %q", note)
			}

			again, _, err := e.Install(installed, cmdLine)
			if err != nil {
				t.Fatalf("second Install: %v", err)
			}
			if string(again) != string(installed) {
				t.Error("Install is not idempotent")
			}

			back, removed, err := e.Uninstall(installed, cmdLine)
			if err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if !removed {
				t.Error("Uninstall did not report removing the hook it had just installed")
			}
			if got := len(back); got == 0 {
				t.Error("Uninstall produced zero bytes")
			}

			_, removedAgain, err := e.Uninstall(back, cmdLine)
			if err != nil {
				t.Fatalf("second Uninstall: %v", err)
			}
			if removedAgain {
				t.Error("Uninstall is not idempotent: it claimed a second removal")
			}
		})
	}
}
