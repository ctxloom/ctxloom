package codex

import (
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// measuredStampCommand and measuredStampHash are ONE REAL OBSERVATION, kept
// verbatim as this package's ground truth for codex's hook identity hash.
//
// Provenance: codex-cli 0.144.4, a scratch CODEX_HOME holding exactly this
// SessionStart hook, asked over the app-server protocol
// (`codex app-server --stdio`, hooks/list) what it thought the hook's identity
// was. It answered with this hash and trustStatus "untrusted"; seeding a
// [hooks.state] record carrying this hash flipped it to "trusted", and
// `codex exec` — with no --dangerously-bypass-hook-trust — then ran the hook.
//
// It is a vector, not an example: hookIdentityHash is a reimplementation of
// someone else's serializer, and the only thing separating a correct one from a
// plausible one is a value codex itself produced.
const (
	// NOTE the REAL newline inside the printf argument. config.toml spelled it
	// `"printf '%s\n' …"`, a TOML basic string, so codex hashed a literal
	// newline byte — writing the two characters backslash-n here instead
	// produces a different, plausible-looking hash. That is precisely the class
	// of mistake this vector exists to catch, so it is spelled out rather than
	// tidied into an escape.
	measuredStampCommand = "printf '%s\n' STAMPED >> /tmp/claude-1000/-home-babbitt-workspace-ctxloom-ctxloom-main/420d8d11-8e3d-4133-934c-cd6dbab959cc/scratchpad/cx/stamp.txt"
	measuredStampHash    = "sha256:579aebfb3d91c7b641422957aff400e75c518d655bb2d4232ad1e136dcf83164"
	measuredStampKey     = "/tmp/claude-1000/-home-babbitt-workspace-ctxloom-ctxloom-main/420d8d11-8e3d-4133-934c-cd6dbab959cc/scratchpad/cx/home/config.toml:session_start:0:0"
)

// TestHookIdentityHash_MatchesCodexMeasuredVector is the anchor: our hash of the
// hook codex looked at equals the hash codex reported for it. Everything else in
// this file tests behaviour AROUND the hash; this tests the hash.
func TestHookIdentityHash_MatchesCodexMeasuredVector(t *testing.T) {
	got, err := hookIdentityHash("session_start", "", false, map[string]any{
		"type":    "command",
		"command": measuredStampCommand,
	})
	if err != nil {
		t.Fatalf("hookIdentityHash: %v", err)
	}
	if got != measuredStampHash {
		t.Errorf("hash does not match the value codex-cli 0.144.4 reported for this hook\n got: %s\nwant: %s", got, measuredStampHash)
	}
}

// TestHookStateKey_MatchesCodexMeasuredVector pins the other half of the record.
// A perfect hash filed under the wrong key trusts nothing, and does so silently.
func TestHookStateKey_MatchesCodexMeasuredVector(t *testing.T) {
	got := hookStateKey("/tmp/claude-1000/-home-babbitt-workspace-ctxloom-ctxloom-main/420d8d11-8e3d-4133-934c-cd6dbab959cc/scratchpad/cx/home/config.toml", "session_start", 0, 0)
	if got != measuredStampKey {
		t.Errorf("key does not match the one codex-cli 0.144.4 reported\n got: %s\nwant: %s", got, measuredStampKey)
	}
}

// TestHookIdentityHash_NormalizationRules covers each normalization the vendor
// applies before hashing. Each case is a way to get a hash that LOOKS fine and
// that codex reads as `modified` — i.e. a hook that silently never runs.
func TestHookIdentityHash_NormalizationRules(t *testing.T) {
	base := map[string]any{"type": "command", "command": "echo hi"}
	hashOf := func(t *testing.T, label, matcher string, hasMatcher bool, entry map[string]any) string {
		t.Helper()
		h, err := hookIdentityHash(label, matcher, hasMatcher, entry)
		if err != nil {
			t.Fatalf("hookIdentityHash: %v", err)
		}
		return h
	}

	t.Run("omitted timeout hashes as codex's 600 default", func(t *testing.T) {
		withDefault := hashOf(t, "session_start", "", false, base)
		explicit := hashOf(t, "session_start", "", false, map[string]any{
			"type": "command", "command": "echo hi", "timeout": 600,
		})
		if withDefault != explicit {
			t.Errorf("an omitted timeout must hash as timeout=600 (unwrap_or(600)); got %s vs %s", withDefault, explicit)
		}
	})

	t.Run("zero timeout is clamped to 1, not hashed as 0", func(t *testing.T) {
		zero := hashOf(t, "session_start", "", false, map[string]any{
			"type": "command", "command": "echo hi", "timeout": 0,
		})
		one := hashOf(t, "session_start", "", false, map[string]any{
			"type": "command", "command": "echo hi", "timeout": 1,
		})
		if zero != one {
			t.Errorf("timeout=0 must hash as codex's .max(1); got %s vs %s", zero, one)
		}
	})

	t.Run("commandWindows is excluded from the identity", func(t *testing.T) {
		with := hashOf(t, "session_start", "", false, map[string]any{
			"type": "command", "command": "echo hi", "commandWindows": "echo hi.exe",
		})
		if without := hashOf(t, "session_start", "", false, base); with != without {
			t.Errorf("commandWindows must not affect the hash (normalized to None); got %s vs %s", with, without)
		}
	})

	t.Run("statusMessage is included in the identity", func(t *testing.T) {
		with := hashOf(t, "session_start", "", false, map[string]any{
			"type": "command", "command": "echo hi", "statusMessage": "working",
		})
		if without := hashOf(t, "session_start", "", false, base); with == without {
			t.Error("statusMessage survives normalization and must change the hash")
		}
	})

	t.Run("matcher changes the identity where codex honours it", func(t *testing.T) {
		with := hashOf(t, "pre_tool_use", "Bash", true, base)
		if without := hashOf(t, "pre_tool_use", "", false, base); with == without {
			t.Error("a matcher on PreToolUse must change the hash")
		}
	})

	t.Run("matcher is dropped for events codex forces to None", func(t *testing.T) {
		for _, label := range []string{"user_prompt_submit", "stop"} {
			with := hashOf(t, label, "Bash", true, base)
			without := hashOf(t, label, "", false, base)
			if with != without {
				t.Errorf("%s ignores matchers (matcher_pattern_for_event), so it must not enter the hash; got %s vs %s", label, with, without)
			}
		}
	})

	t.Run("event name is part of the identity", func(t *testing.T) {
		a := hashOf(t, "session_start", "", false, base)
		if b := hashOf(t, "post_tool_use", "", false, base); a == b {
			t.Error("the same command under two events must not share a trust record")
		}
	})
}

// TestHookIdentityHash_RefusesWhatItCannotHash: a hash it cannot compute
// correctly must be an error, never a guess. A guessed hash is written to disk,
// read by codex as `modified`, and the hook silently does not run — the exact
// failure mode this package exists to prevent, reintroduced by a fallback.
func TestHookIdentityHash_RefusesWhatItCannotHash(t *testing.T) {
	for name, entry := range map[string]map[string]any{
		"prompt handler":  {"type": "prompt"},
		"agent handler":   {"type": "agent"},
		"no type":         {"command": "echo hi"},
		"no command":      {"type": "command"},
		"empty command":   {"type": "command", "command": ""},
		"non-int timeout": {"type": "command", "command": "echo hi", "timeout": "30s"},
		"float timeout":   {"type": "command", "command": "echo hi", "timeout": 1.5},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := hookIdentityHash("session_start", "", false, entry); err == nil {
				t.Error("expected a refusal, got a hash")
			}
		})
	}
}

// TestJSONString_MatchesSerdeEscaping pins the escaping rules the hash rides on.
// encoding/json would pass three of these and produce a different byte string,
// and the only symptom would be hooks that never run.
func TestJSONString_MatchesSerdeEscaping(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"redirect is not HTML-escaped": {"a > b < c & d", `"a > b < c & d"`},
		"quote and backslash":          {`say "hi"\n`, `"say \"hi\"\\n"`},
		"real newline and tab":         {"a\nb\tc", `"a\nb\tc"`},
		"other control byte":           {"a\x01b", `"a\u0001b"`},
		"line separator stays literal": {"a\u2028b", "\"a\u2028b\""},
		"non-ascii stays literal":      {"café", `"café"`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := jsonString(tc.in); got != tc.want {
				t.Errorf("jsonString(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestSeedHookTrust_SeedsEveryCommandHookByPosition checks the walk: every hook
// gets a record, and the record is filed under the positional key codex will
// compute for it.
func TestSeedHookTrust_SeedsEveryCommandHookByPosition(t *testing.T) {
	cfg := map[string]any{"hooks": map[string]any{
		"SessionStart": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "first"}}},
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "second"}}},
		},
		"PreToolUse": []any{
			map[string]any{"matcher": "Bash", "hooks": []any{
				map[string]any{"type": "command", "command": "third"},
				map[string]any{"type": "command", "command": "fourth"},
			}},
		},
	}}

	seeded, unseedable := seedHookTrust(cfg, "/home/u/.codex/config.toml")
	if seeded != 4 || len(unseedable) != 0 {
		t.Fatalf("seeded=%d unseedable=%v, want 4 and none", seeded, unseedable)
	}

	state := asMap(asMap(cfg["hooks"])[hookStateTable])
	for _, key := range []string{
		"/home/u/.codex/config.toml:session_start:0:0",
		"/home/u/.codex/config.toml:session_start:1:0",
		"/home/u/.codex/config.toml:pre_tool_use:0:0",
		"/home/u/.codex/config.toml:pre_tool_use:0:1",
	} {
		rec := asMap(state[key])
		if rec == nil {
			t.Errorf("no trust record under %s", key)
			continue
		}
		if rec["enabled"] != true {
			t.Errorf("%s: enabled = %v, want true", key, rec["enabled"])
		}
		hash, _ := rec["trusted_hash"].(string)
		if !strings.HasPrefix(hash, hookTrustHashPrefix) || len(hash) != len(hookTrustHashPrefix)+64 {
			t.Errorf("%s: trusted_hash = %q, want a %s… sha256 digest", key, hash, hookTrustHashPrefix)
		}
	}
	if len(state) != 4 {
		t.Errorf("state holds %d records, want exactly 4", len(state))
	}
}

// TestSeedHookTrust_PrunesOurStaleRecordsAndKeepsForeignOnes: the seed is
// authoritative over its OWN file's keys and touches nobody else's. A stale
// record of ours would describe a hook that no longer exists; a foreign one
// belongs to a source we do not manage.
func TestSeedHookTrust_PrunesOurStaleRecordsAndKeepsForeignOnes(t *testing.T) {
	cfg := map[string]any{"hooks": map[string]any{
		"SessionStart": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "kept"}}},
		},
		hookStateTable: map[string]any{
			"/home/u/.codex/config.toml:session_start:7:0": map[string]any{"enabled": true, "trusted_hash": "sha256:stale"},
			"/home/u/.codex/hooks.json:session_start:0:0":  map[string]any{"enabled": true, "trusted_hash": "sha256:foreign"},
		},
	}}

	if _, unseedable := seedHookTrust(cfg, "/home/u/.codex/config.toml"); len(unseedable) != 0 {
		t.Fatalf("unexpected unseedable: %v", unseedable)
	}

	state := asMap(asMap(cfg["hooks"])[hookStateTable])
	if _, stillThere := state["/home/u/.codex/config.toml:session_start:7:0"]; stillThere {
		t.Error("a stale record for a hook that no longer exists survived the reseed")
	}
	if _, kept := state["/home/u/.codex/hooks.json:session_start:0:0"]; !kept {
		t.Error("a trust record keyed to another source was removed; it is not ours to touch")
	}
	if _, fresh := state["/home/u/.codex/config.toml:session_start:0:0"]; !fresh {
		t.Error("the surviving hook was not re-seeded")
	}
}

// TestSeedHookTrust_ReportsWhatItCannotVouchFor: hooks that will not run must
// come back named, so the caller can say so. Silence here is the whole bug.
func TestSeedHookTrust_ReportsWhatItCannotVouchFor(t *testing.T) {
	cfg := map[string]any{"hooks": map[string]any{
		"SessionStart": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "prompt"}}},
		},
		"NotAnEvent": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "never runs"}}},
		},
	}}

	seeded, unseedable := seedHookTrust(cfg, "/home/u/.codex/config.toml")
	if seeded != 0 {
		t.Errorf("seeded = %d, want 0", seeded)
	}
	if len(unseedable) != 2 {
		t.Fatalf("unseedable = %v, want one entry per unrunnable hook", unseedable)
	}
	joined := strings.Join(unseedable, "\n")
	if !strings.Contains(joined, "NotAnEvent") || !strings.Contains(joined, "SessionStart") {
		t.Errorf("both hooks must be named in the report; got %s", joined)
	}
}

// TestSeedHookTrust_LeavesNoEmptyStateTable: an empty [hooks.state] in a file
// that has no hooks is noise in the user's config, and noise in a security
// section reads as a claim.
func TestSeedHookTrust_LeavesNoEmptyStateTable(t *testing.T) {
	cfg := map[string]any{"hooks": map[string]any{
		hookStateTable: map[string]any{
			"/home/u/.codex/config.toml:session_start:0:0": map[string]any{"enabled": true},
		},
	}}
	seedHookTrust(cfg, "/home/u/.codex/config.toml")
	if _, present := asMap(cfg["hooks"])[hookStateTable]; present {
		t.Error("an emptied [hooks.state] table must be removed, not left behind")
	}
}

// --- the writer seam -------------------------------------------------------

// TestWriteSettingsIn_SeedsHookTrustOnAnOwnedHome is the test the mutation
// targets: break the seed emission and this goes red. It asserts the FILE, not
// an intermediate — the payload is the whole point.
func TestWriteSettingsIn_SeedsHookTrustOnAnOwnedHome(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "echo seeded", Type: "command"}},
	}}

	if err := w.writeSettingsIn(hooks, nil, nil, "/owned/home", "/work/dir"); err != nil {
		t.Fatalf("writeSettingsIn: %v", err)
	}

	path := "/owned/home/.codex/config.toml"
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	got := string(data)

	// The record has to be filed under the path codex will resolve, which is
	// the file's own path — not the home, not the workdir.
	wantKey := path + ":session_start:0:0"
	if !strings.Contains(got, wantKey) {
		t.Errorf("no [hooks.state] record keyed %q in:\n%s", wantKey, got)
	}
	if !strings.Contains(got, "trusted_hash") || !strings.Contains(got, hookTrustHashPrefix) {
		t.Errorf("no trusted_hash written; codex will skip this hook. config:\n%s", got)
	}
	// Workspace trust is the OTHER gate and must still be answered.
	if !strings.Contains(got, `trust_level = 'trusted'`) && !strings.Contains(got, `trust_level = "trusted"`) {
		t.Errorf("the workspace trust pre-seed regressed; config:\n%s", got)
	}
}

// TestWriteSettingsIn_NoHookTrustWithoutAnOwnedHome is the security pin: the
// seed is ctxloom answering a security prompt, and it may only ever do that for
// a home ctxloom itself provisioned. Emit it when trustAbsPath is empty — the
// not-our-home signal — and this goes red.
func TestWriteSettingsIn_NoHookTrustWithoutAnOwnedHome(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "echo unseeded", Type: "command"}},
	}}

	if err := w.writeSettingsIn(hooks, nil, nil, "/not/ours", ""); err != nil {
		t.Fatalf("writeSettingsIn: %v", err)
	}

	data, err := afero.ReadFile(fs, "/not/ours/.codex/config.toml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "trusted_hash") || strings.Contains(got, hookStateTable+".") {
		t.Errorf("ctxloom recorded hook trust in a home it does not own:\n%s", got)
	}
	if strings.Contains(got, "trust_level") {
		t.Errorf("ctxloom recorded workspace trust in a home it does not own:\n%s", got)
	}
}

// TestRemoveSettingsIn_RevertsOurTrustRecords: the revert must take ctxloom's
// answers with it, and leave another source's alone.
func TestRemoveSettingsIn_RevertsOurTrustRecords(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "ctxloom hook run", Type: "command"}},
	}}
	if err := w.writeSettingsIn(hooks, nil, nil, "/owned/home", "/work/dir"); err != nil {
		t.Fatalf("writeSettingsIn: %v", err)
	}
	if err := w.removeSettingsIn("/owned/home"); err != nil {
		t.Fatalf("removeSettingsIn: %v", err)
	}
	data, err := afero.ReadFile(fs, "/owned/home/.codex/config.toml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "trusted_hash") {
		t.Errorf("a trust record survived the revert:\n%s", string(data))
	}
}

// --- the fail-loud sibling -------------------------------------------------

// TestWriteSettingsIn_SaysSoWhenAHookCannotBeVouchedFor: a hook that reaches
// config.toml and will not run has to be ANNOUNCED. Nothing downstream can
// detect it — codex exits 0, the session works, the hook is just absent — so
// this warning is the only signal that exists. Silence it and this goes red.
func TestWriteSettingsIn_SaysSoWhenAHookCannotBeVouchedFor(t *testing.T) {
	var buf strings.Builder
	restore := clidiag.SetSink(&buf)
	defer restore()

	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	// A backend passthrough hook under an event codex does not have: written
	// faithfully, dropped by codex's own deserializer, never runs.
	hooks := &wire.HooksConfig{Plugins: map[string]wire.BackendHooks{
		"codex": {"NotAnEvent": []wire.Hook{{Command: "echo doomed", Type: "command"}}},
	}}
	if err := w.writeSettingsIn(hooks, nil, nil, "/owned/home", "/work/dir"); err != nil {
		t.Fatalf("writeSettingsIn: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "NotAnEvent") {
		t.Errorf("the warning must name the hook that will not run; got:\n%s", got)
	}
	if !strings.Contains(got, "SKIP") {
		t.Errorf("the warning must say the hook will be skipped; got:\n%s", got)
	}
	if !strings.Contains(got, "/owned/home/.codex/config.toml") {
		t.Errorf("the warning must name the file it is talking about; got:\n%s", got)
	}
}

// TestWriteSettingsIn_SaysSoWhenNoHookCanBeVouchedFor is the whole-file arm:
// hooks written into a home ctxloom does not own get no trust records, so NONE
// of them run. Same reasoning, same silence to break.
func TestWriteSettingsIn_SaysSoWhenNoHookCanBeVouchedFor(t *testing.T) {
	var buf strings.Builder
	restore := clidiag.SetSink(&buf)
	defer restore()

	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "echo a", Type: "command"}},
		PreTool:      []wire.Hook{{Command: "echo b", Type: "command"}},
	}}
	if err := w.writeSettingsIn(hooks, nil, nil, "/not/ours", ""); err != nil {
		t.Fatalf("writeSettingsIn: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "all 2 hook(s)") {
		t.Errorf("the warning must count the hooks it is talking about; got:\n%s", got)
	}
	if !strings.Contains(got, "config_home: project") {
		t.Errorf("the warning must name the remedy, as its sibling refusals do; got:\n%s", got)
	}
}

// TestWriteSettingsIn_QuietWhenEveryHookIsVouchedFor keeps the warning
// meaningful. A skip warning that fires on healthy runs is noise, and noise is
// how a real one gets scrolled past.
func TestWriteSettingsIn_QuietWhenEveryHookIsVouchedFor(t *testing.T) {
	var buf strings.Builder
	restore := clidiag.SetSink(&buf)
	defer restore()

	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "echo fine", Type: "command"}},
	}}
	if err := w.writeSettingsIn(hooks, nil, nil, "/owned/home", "/work/dir"); err != nil {
		t.Fatalf("writeSettingsIn: %v", err)
	}
	if strings.Contains(buf.String(), "SKIP") {
		t.Errorf("nothing was skipped; the warning must not fire. got:\n%s", buf.String())
	}
}

// TestSeedHookTrust_IsStableAcrossRewrites: writing the same hooks twice must
// produce the same records. If it did not, every re-delivery would invalidate
// the trust it had just established and the hooks would stop running.
func TestSeedHookTrust_IsStableAcrossRewrites(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "echo a", Type: "command"}},
		PreTool:      []wire.Hook{{Command: "echo b", Type: "command", Matcher: "Bash"}},
	}}

	read := func() string {
		t.Helper()
		if err := w.writeSettingsIn(hooks, nil, nil, "/owned/home", "/work/dir"); err != nil {
			t.Fatalf("writeSettingsIn: %v", err)
		}
		data, err := afero.ReadFile(fs, "/owned/home/.codex/config.toml")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(data)
	}

	if first, second := read(), read(); first != second {
		t.Errorf("a re-delivery changed the config:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}
