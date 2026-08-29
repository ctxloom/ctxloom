//go:build parked_engines

package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// VENDOR PIN — internal/codex's hook-trust seed against the INSTALLED codex.
//
// seedHookTrust writes into a surface codex does not document and does not
// promise: `[hooks.state."<key>"] trusted_hash`, whose key format upstream
// itself marks as provisional ("TODO(abhinav): replace this positional suffix
// with a durable hook id") and whose hash is a private normalization. That is
// the same exposure as the CLAUDE_SECURESTORAGE_CONFIG_DIR redirect, and it
// gets the same protection: a pin the release / new-client capability job runs,
// so a codex upgrade that moves this surface is caught by a red test rather
// than by a user wondering why their hooks stopped.
//
// WHAT MAKES THIS A PIN AND NOT A UNIT TEST. hooktrust_test.go checks our
// arithmetic against a hash recorded once. These tests ask the codex binary
// that is installed RIGHT NOW to grade the file ctxloom's real writer produced.
// Both layers are needed: the recorded vector catches us breaking our own code,
// this catches codex changing underneath it.
//
// THE DANGEROUS FAILURE IS THE QUIET ONE. If codex renames the table, the seed
// still parses as an unknown key, we still write it, codex still ignores it,
// and every hook goes back to being silently skipped. Nothing errors. Only
// asking codex for its verdict — trustStatus — catches that, which is why the
// primary pin reads the vendor's own answer instead of comparing bytes.
//
// COST: the schema/verdict pin is FREE. It drives `codex app-server --stdio`,
// which resolves hooks locally and needs no credentials and no model turn
// (verified: hooks/list answers in a CODEX_HOME with no auth.json). The live
// firing pin buys one small turn and is therefore opt-in.

// vendorPinCodexBinary resolves the codex under test.
//
// Absent codex is a SKIP by default (most developers have none) and a FAILURE
// when CTXLOOM_VENDOR_PIN=require — the capability job sets that, because a
// pin that silently skips in CI is a pin that protects nothing.
func vendorPinCodexBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("codex")
	if err == nil {
		return path
	}
	if os.Getenv("CTXLOOM_VENDOR_PIN") == "require" {
		t.Fatalf("CTXLOOM_VENDOR_PIN=require but no codex binary is on PATH (%v). This lane exists to grade ctxloom's hook-trust seed against a real codex; with no codex it grades nothing. Install codex on the runner or drop the require flag.", err)
	}
	t.Skip("no codex binary on PATH; set CTXLOOM_VENDOR_PIN=require to make this a failure")
	return ""
}

// vendorPinHookTrustFailure is the documented response to a red pin, carried in
// the test's own failure text so whoever sees it first does not have to go
// looking for the runbook.
const vendorPinHookTrustFailure = `
WHAT THIS MEANS: ctxloom's hook-trust seed no longer satisfies the installed
codex. codex runs a hook only once it has been told to trust that hook, so as
of this codex version EVERY ctxloom hook delivered to a codex agent is silently
skipped again — the run still exits 0, the session still works, and no hook
fires. The fail-loud warn in seedHookTrust's caller cannot see this: from
ctxloom's side the seed was written successfully.

WHAT TO DO: re-verify the surface against this codex version before shipping.
  1. Write a hook into a scratch CODEX_HOME and ask codex what it thinks:
     codex app-server --stdio  ->  initialize, then hooks/list.
     The reply's key / currentHash / trustStatus are the ground truth.
  2. Compare against internal/codex/hooktrust.go's hookStateKey and
     hookIdentityHash, whose upstream sources are cited by symbol in that
     file's header (tag rust-v0.144.4).
  3. Re-record the measured vector in hooktrust_test.go from the new reply.
UNTIL THAT IS DONE, codex hooks are dead. Say so in the release notes.`

// vendorPinSeedHome writes a real config.toml through ctxloom's PRODUCTION
// writer (not a hand-built fixture) into a scratch CODEX_HOME, and returns the
// home parent, the project dir, and the stamp path the hook targets.
//
// The stamp lives outside the project dir on purpose, for the same reason the
// P3 hook-firing probe puts it there: inside the cwd the agent could reach it,
// and the pin would be measuring reachability rather than hook execution.
func vendorPinSeedHome(t *testing.T) (homeParent, projDir, stampPath string) {
	t.Helper()
	root := t.TempDir()
	homeParent = filepath.Join(root, "home")
	projDir = filepath.Join(root, "proj")
	stampPath = filepath.Join(root, "stamp.txt")
	for _, d := range []string{homeParent, projDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	w := &CodexHookWriter{FS: afero.NewOsFs()}
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{
			Type:    "command",
			Command: "printf '%s\n' STAMPED >> " + stampPath,
		}},
	}}
	if err := w.writeSettingsIn(hooks, nil, homeParent, projDir); err != nil {
		t.Fatalf("writeSettingsIn: %v", err)
	}
	return homeParent, projDir, stampPath
}

// vendorPinSeedCredentials copies the host's codex credentials into the scratch
// home, which is exactly what ctxloom's own owned-home axis does before a run
// (ensureCodexCredentials) — the live pin has to authenticate the same way a
// real ctxloom-provisioned home does, or it is not measuring that path.
//
// Read-only against the real ~/.codex: copied OUT of, never written to.
func vendorPinSeedCredentials(t *testing.T, homeParent string) {
	t.Helper()
	hostHome, err := hostCodexHome()
	if err != nil {
		t.Skipf("cannot resolve the host codex home to borrow credentials from: %v", err)
	}
	auth, err := os.ReadFile(filepath.Join(hostHome, "auth.json"))
	if err != nil {
		t.Skipf("no host codex credentials to seed the scratch home with (%v); `codex login` first", err)
	}
	if err := os.WriteFile(filepath.Join(homeParent, ConfigDirName, "auth.json"), auth, 0o600); err != nil {
		t.Fatalf("seed auth.json: %v", err)
	}
}

// codexHookMetadata is the subset of codex's HookMetadata this pin reads.
type codexHookMetadata struct {
	Key         string `json:"key"`
	Command     string `json:"command"`
	CurrentHash string `json:"currentHash"`
	TrustStatus string `json:"trustStatus"`
	EventName   string `json:"eventName"`
}

// vendorPinHooksList drives `codex app-server --stdio` and returns what codex
// says about the hooks in codexHome for cwd. This is codex's OWN answer, over
// its own protocol — the closest thing to asking the vendor directly.
func vendorPinHooksList(t *testing.T, bin, codexHome, cwd string) []codexHookMetadata {
	t.Helper()

	cmd := exec.Command(bin, "app-server", "--stdio")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CODEX_HOME="+filepath.Join(codexHome, ConfigDirName))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start codex app-server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	for _, msg := range []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
			"clientInfo": map[string]any{"name": "ctxloom-vendor-pin", "version": "1"},
		}},
		map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "hooks/list", "params": map[string]any{
			"cwds": []string{cwd},
		}},
	} {
		line, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		if _, err := stdin.Write(append(line, '\n')); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}

	type reply struct {
		hooks []codexHookMetadata
		err   error
	}
	done := make(chan reply, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			var env struct {
				ID     json.RawMessage `json:"id"`
				Result struct {
					Data []struct {
						Hooks []codexHookMetadata `json:"hooks"`
					} `json:"data"`
				} `json:"result"`
			}
			if json.Unmarshal(scanner.Bytes(), &env) != nil || string(env.ID) != "2" {
				continue
			}
			var all []codexHookMetadata
			for _, entry := range env.Result.Data {
				all = append(all, entry.Hooks...)
			}
			done <- reply{hooks: all}
			return
		}
		done <- reply{err: scanner.Err()}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("reading codex app-server: %v", r.err)
		}
		return r.hooks
	case <-time.After(90 * time.Second):
		t.Fatal("codex app-server did not answer hooks/list within 90s")
		return nil
	}
}

// TestVendorPin_CodexTrustsWhatWeSeed is THE pin: ctxloom's production writer
// produces a config.toml, and the installed codex grades every hook in it
// "trusted". Red means codex will skip ctxloom's hooks.
func TestVendorPin_CodexTrustsWhatWeSeed(t *testing.T) {
	bin := vendorPinCodexBinary(t)
	homeParent, projDir, _ := vendorPinSeedHome(t)

	hooks := vendorPinHooksList(t, bin, homeParent, projDir)
	if len(hooks) == 0 {
		t.Fatalf("codex reports NO hooks at all for the config.toml ctxloom just wrote — the [hooks] shape itself is no longer being read.%s", vendorPinHookTrustFailure)
	}
	for _, h := range hooks {
		if h.TrustStatus != "trusted" {
			t.Errorf("codex grades hook %q (%s) as %q, not \"trusted\" — it will not run.\ncodex's key:  %s\ncodex's hash: %s%s",
				h.Command, h.EventName, h.TrustStatus, h.Key, h.CurrentHash, vendorPinHookTrustFailure)
		}
	}
}

// TestVendorPin_OurKeyAndHashMatchCodex localizes a failure of the pin above.
// "trusted" can only be reached by getting BOTH halves right; when it is not
// reached, this says which half moved, which is the difference between a
// ten-minute fix and an afternoon.
func TestVendorPin_OurKeyAndHashMatchCodex(t *testing.T) {
	bin := vendorPinCodexBinary(t)
	homeParent, projDir, _ := vendorPinSeedHome(t)
	settingsPath := filepath.Join(homeParent, ConfigDirName, ConfigFileName)

	for _, h := range vendorPinHooksList(t, bin, homeParent, projDir) {
		label, known := hookEventKeyLabels[hookEventTableNameFor(h.EventName)]
		if !known {
			t.Errorf("codex reports hook event %q, which hookEventKeyLabels does not map — a new or renamed event.%s", h.EventName, vendorPinHookTrustFailure)
			continue
		}
		if wantKey := hookStateKey(settingsPath, label, 0, 0); h.Key != wantKey {
			t.Errorf("KEY FORMAT MOVED (hook_key).\ncodex: %s\n  our: %s%s", h.Key, wantKey, vendorPinHookTrustFailure)
		}
		gotHash, err := hookIdentityHash(label, "", false, map[string]any{
			"type": "command", "command": h.Command,
		})
		if err != nil {
			t.Fatalf("hookIdentityHash: %v", err)
		}
		if gotHash != h.CurrentHash {
			t.Errorf("IDENTITY HASH MOVED (command_hook_hash).\ncodex: %s\n  our: %s%s", h.CurrentHash, gotHash, vendorPinHookTrustFailure)
		}
	}
}

// hookEventTableNameFor maps codex's WIRE spelling of an event (camelCase, as
// hooks/list reports it) back to the config.toml table name ctxloom writes.
// Only the pin needs this direction; the writer never sees the wire spelling.
func hookEventTableNameFor(wire string) string {
	for table, label := range hookEventKeyLabels {
		if wire == label || strings.EqualFold(strings.ReplaceAll(label, "_", ""), wire) {
			return table
		}
	}
	return wire
}

// TestVendorPin_HookTrustGateStillExists is the cheap corroborating check: the
// gate this whole mechanism answers is still a thing this codex has. If the
// bypass flag disappears, the gate may have been removed (making the seed
// harmless but pointless) or reworked (making it wrong) — either way the model
// in hooktrust.go needs re-reading before the next release.
func TestVendorPin_HookTrustGateStillExists(t *testing.T) {
	bin := vendorPinCodexBinary(t)
	out, err := exec.Command(bin, "exec", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("codex exec --help: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "dangerously-bypass-hook-trust") {
		t.Errorf("`codex exec --help` no longer mentions --dangerously-bypass-hook-trust, so this codex's hook-trust gate is not the one internal/codex/hooktrust.go was measured against.%s", vendorPinHookTrustFailure)
	}
}

// TestVendorPin_CodexRunsOurSeededHook is the BEHAVIOURAL pin — the one that
// catches a seed which parses, grades as trusted, and still does not make the
// hook run. It buys one small model turn, so it is opt-in
// (CTXLOOM_VENDOR_PIN_LIVE=1) and lives here rather than on any default path.
//
// Deliberately no --dangerously-bypass-hook-trust: the flag would make this
// pass no matter what the seed did, which is the one thing it must not do.
func TestVendorPin_CodexRunsOurSeededHook(t *testing.T) {
	if os.Getenv("CTXLOOM_VENDOR_PIN_LIVE") != "1" {
		t.Skip("live pin buys a codex turn; set CTXLOOM_VENDOR_PIN_LIVE=1 to run it")
	}
	bin := vendorPinCodexBinary(t)
	homeParent, projDir, stampPath := vendorPinSeedHome(t)
	vendorPinSeedCredentials(t, homeParent)

	if _, err := os.Stat(stampPath); err == nil {
		t.Fatalf("%s exists before the run; the hook is the only thing that may create it", stampPath)
	}

	cmd := exec.Command(bin, "exec", "--skip-git-repo-check", "-C", projDir, "Reply with exactly: OK")
	cmd.Dir = projDir
	cmd.Env = append(os.Environ(), "CODEX_HOME="+filepath.Join(homeParent, ConfigDirName))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("codex exec: %v\n%s", err, out)
	}
	if _, err := os.Stat(stampPath); err != nil {
		t.Errorf("codex exec succeeded but the SessionStart hook never wrote %s — the seed no longer makes hooks run.\ncodex output:\n%s%s", stampPath, out, vendorPinHookTrustFailure)
	}
}
