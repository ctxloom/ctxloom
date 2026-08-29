package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// HOOK TRUST: the second, independent codex gate a written hook must clear.
//
// codex has TWO gates between a config.toml ctxloom wrote and a hook that
// actually runs, and clearing one says nothing about the other:
//
//  1. WORKSPACE trust — `[projects."<cwd>"] trust_level = "trusted"`, answered
//     by addProjectTrust (settings.go). About the DIRECTORY codex runs in.
//  2. HOOK trust — `[hooks.state."<key>"] trusted_hash = "sha256:…"`, answered
//     here. About each individual HOOK COMMAND. A hook whose recorded hash does
//     not match what the config now says is `untrusted` (never recorded) or
//     `modified` (recorded, then edited), and codex SKIPS IT. Interactively it
//     first prompts ("Hooks need review … Continue without trusting (hooks
//     won't run)", tui/src/startup_hooks_review.rs); under `codex exec` there is
//     nobody to prompt, so the hook is skipped in silence — exit 0, a live
//     session, and none of ctxloom's hooks ran. This project's signature
//     failure, arriving from the vendor side.
//
// MEASURED, not read. On codex-cli 0.144.4, a scratch CODEX_HOME whose
// config.toml carried a stamp-writing SessionStart hook and workspace trust:
// `codex exec` left NO stamp. Adding only the `[hooks.state]` record below —
// no --dangerously-bypass-hook-trust, nothing else changed — made the same
// command write the stamp and print "hook: SessionStart Completed". That
// differential is what this file exists to reproduce, and TestHookTrust_* plus
// the test-vendor-codex pin (hooktrust_vendor_test.go) are what keep it true.
//
// WHY SEEDING AND NOT --dangerously-bypass-hook-trust. The bypass flag exists
// and would work, but it turns the gate off for EVERY hook in the home,
// including any that arrived some other way, and it is a run-time argument
// rather than a durable fact about the config ctxloom wrote. Seeding the
// vendor's own persisted record is the same answer the vendor's own prompt
// writes, scoped to exactly the hooks in exactly this file — the house rule
// (integrate through the vendor's native surface) and the narrower blast
// radius pick the same arm.
//
// THE BOUNDARY IS THE SAME ONE PROJECT TRUST DRAWS. Seeding happens only where
// addProjectTrust already fires — a codex home ctxloom itself provisioned
// (trustAbsPath non-empty; see writeSettingsIn and backend.go's Setup). It never
// happens for the user's real ~/.codex, which ctxloom does not write at all
// (deliveryHome's homeIsHostOwned arm). docs/trust-model.md is normative.
//
// UPSTREAM (openai/codex, tag rust-v0.144.4), cited by symbol so a rename
// fails loud rather than a line number drifting silently:
//   - codex-rs/config/src/hook_config.rs — HooksToml.state, HookStateToml
//     {enabled, trusted_hash}, MatcherGroup, HookHandlerConfig::Command
//     (field renames: timeout_sec→"timeout", command_windows→"commandWindows",
//     status_message→"statusMessage").
//   - codex-rs/hooks/src/lib.rs — hook_key, hook_event_key_label.
//   - codex-rs/hooks/src/engine/discovery.rs — command_hook_hash,
//     NormalizedHookIdentity, hook_trust_status, append_matcher_groups.
//   - codex-rs/config/src/fingerprint.rs — version_for_toml, canonical_json.
//   - codex-rs/hooks/src/events/common.rs — matcher_pattern_for_event.

// hookStateTable is HooksToml's `state` field: the key inside codex's [hooks]
// table that holds per-hook trust records rather than an event's matcher
// groups. Every walk of [hooks] must skip it — it is a table, not an
// array-of-tables, and treating it as an event would be a category error.
const hookStateTable = "state"

// hookTrustHashPrefix is the algorithm tag version_for_toml prepends. Kept
// separate from the hex so a future vendor algorithm change is a visible
// mismatch rather than a silently wrong 64 characters.
const hookTrustHashPrefix = "sha256:"

// defaultHookTimeoutSec is codex's `timeout_sec.unwrap_or(600).max(1)` — the
// value that lands in the identity hash when config.toml omits `timeout`.
// ctxloom omits it for every hook that does not set one (addHook), so this
// constant is on the MAIN path, not an edge case.
const defaultHookTimeoutSec = 600

// hookEventKeyLabels maps codex's config.toml event-table name (the key ctxloom
// writes under [hooks], HookEventsToml's serde renames) to the snake_case label
// codex uses in both the persisted trust key and the identity hash
// (hook_event_key_label). A [hooks.X] whose X is absent here is an event codex
// does not know: it is dropped by codex's own deserializer and can never run,
// which seedHookTrust reports rather than silently failing to seed.
var hookEventKeyLabels = map[string]string{
	"PreToolUse":        "pre_tool_use",
	"PermissionRequest": "permission_request",
	"PostToolUse":       "post_tool_use",
	"PreCompact":        "pre_compact",
	"PostCompact":       "post_compact",
	"SessionStart":      "session_start",
	"UserPromptSubmit":  "user_prompt_submit",
	"SubagentStart":     "subagent_start",
	"SubagentStop":      "subagent_stop",
	"Stop":              "stop",
}

// matcherlessHookEvents are the events for which codex forces the matcher to
// None before hashing (matcher_pattern_for_event). A `matcher` written under
// one of them is inert AND absent from the identity — including it in the hash
// would produce a record codex never matches, i.e. a seed that looks written
// and trusts nothing.
var matcherlessHookEvents = map[string]bool{
	"user_prompt_submit": true,
	"stop":               true,
}

// hookStateKey builds codex's persisted trust key: hook_key's
// "{key_source}:{event_label}:{group_index}:{handler_index}". key_source for a
// config.toml layer is that file's absolute path (discovery.rs's
// ConfigLayerSource arm), so the key is home-specific by construction — a
// seeded record cannot travel to another home and trust a hook there.
//
// The trailing indices are POSITIONAL in the file, and upstream says so
// ("TODO(abhinav): replace this positional suffix with a durable hook id").
// That is why seedHookTrust walks the FINAL table after every add and removal
// rather than recording keys as hooks are appended: reordering one event's
// groups changes the keys of all of them.
func hookStateKey(sourcePath, eventLabel string, groupIndex, handlerIndex int) string {
	return sourcePath + ":" + eventLabel + ":" + strconv.Itoa(groupIndex) + ":" + strconv.Itoa(handlerIndex)
}

// hookIdentityHash reproduces command_hook_hash for ONE command handler in one
// matcher group: sha256 over the compact, key-sorted JSON of a normalized
// identity, tagged "sha256:".
//
// The identity is deliberately NOT the config text. Upstream normalizes so that
// the same hook written two different ways converges on one trust record
// ("Hash a normalized, config-derived identity instead of source text"), which
// is also why re-writing an unchanged config.toml here does not invalidate the
// seed. The normalization this must match exactly:
//
//   - the handler is reduced to ONE entry (its own group), so a group holding
//     three hooks hashes as three separate one-hook groups;
//   - commandWindows is dropped unconditionally (upstream sets it to None in
//     normalized_handler, after resolving it into `command` on Windows);
//   - timeout becomes the effective seconds, unwrap_or(600).max(1) — an omitted
//     or zero timeout is NOT omitted from the hash;
//   - async is always present, even when false;
//   - statusMessage survives if set, and is omitted when absent (Rust None does
//     not serialize into a TOML table);
//   - matcher is the POST-matcher_pattern_for_event value: absent for
//     user_prompt_submit/stop whatever the file says, else the group's.
//
// It reports an error rather than a wrong hash for anything it does not
// recognize (a prompt/agent handler, a non-string command): a wrong hash is a
// record codex reads as `modified` and skips — the silent failure this whole
// file exists to close, dressed up as a success.
func hookIdentityHash(eventLabel string, matcher string, hasMatcher bool, entry map[string]any) (string, error) {
	typ, _ := entry["type"].(string)
	if typ != "command" {
		if typ == "" {
			return "", fmt.Errorf("hook entry has no %q", "type")
		}
		return "", fmt.Errorf("hook handler type %q is not a command hook", typ)
	}
	command, ok := entry["command"].(string)
	if !ok || command == "" {
		return "", fmt.Errorf("hook entry has no command string")
	}

	timeout := defaultHookTimeoutSec
	if raw, present := entry["timeout"]; present {
		v, ok := hookTimeoutSeconds(raw)
		if !ok {
			return "", fmt.Errorf("hook timeout %v is not an integer number of seconds", raw)
		}
		timeout = v
	}
	if timeout < 1 {
		timeout = 1
	}

	async, _ := entry["async"].(bool)

	handler := []jsonField{
		{"async", jsonBool(async)},
		{"command", jsonString(command)},
	}
	if status, ok := entry["statusMessage"].(string); ok {
		handler = append(handler, jsonField{"statusMessage", jsonString(status)})
	}
	handler = append(handler,
		jsonField{"timeout", strconv.Itoa(timeout)},
		jsonField{"type", jsonString("command")},
	)

	identity := []jsonField{
		{"event_name", jsonString(eventLabel)},
		{"hooks", "[" + canonicalObject(handler) + "]"},
	}
	if hasMatcher && !matcherlessHookEvents[eventLabel] {
		identity = append(identity, jsonField{"matcher", jsonString(matcher)})
	}

	sum := sha256.Sum256([]byte(canonicalObject(identity)))
	return hookTrustHashPrefix + hex.EncodeToString(sum[:]), nil
}

// hookTimeoutSeconds coerces a config.toml `timeout` to seconds. The value
// reaches here either freshly built by addHook (a Go int) or round-tripped
// through go-toml (an int64), and a user may have written a float; anything
// that is not a whole number of seconds is refused rather than truncated,
// because a truncated timeout hashes to a record codex will not match.
func hookTimeoutSeconds(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		if v != float64(int64(v)) {
			return 0, false
		}
		return int(v), true
	default:
		return 0, false
	}
}

// seedHookTrust records ctxloom's answer to codex's hook-trust prompt for every
// command hook currently in cfg's [hooks] table, writing
// `[hooks.state."<key>"] enabled = true, trusted_hash = "sha256:…"`.
//
// sourcePath must be the ABSOLUTE path of the config.toml cfg will be written
// to — it is the first field of every key codex computes, so a relative or
// wrong path produces records that parse, look right, and trust nothing.
//
// It walks the FINAL table, after removeManagedHooks/addUnifiedHooks/
// addBackendHooks have settled, because the key's group index is positional:
// recording a key at the moment a hook is appended would be correct only until
// the next hook shifted it.
//
// STALE RECORDS ARE PRUNED, and only ours. Entries keyed by THIS config.toml
// are fully determined by this walk, so any that survive it describe hooks that
// no longer exist and are removed — otherwise a shrinking hook set leaves
// records that would silently pre-trust whatever later lands on the same index.
// Entries keyed by anything else (another layer's hooks.json, a plugin) are
// another source's business and are passed through untouched.
//
// It returns what it could NOT seed, so the caller can say so out loud: those
// hooks are in the file and will never run.
func seedHookTrust(cfg map[string]any, sourcePath string) (seeded int, unseedable []string) {
	hooks := asMap(cfg["hooks"])
	if hooks == nil {
		return 0, nil
	}

	state := asMap(hooks[hookStateTable])
	if state == nil {
		state = map[string]any{}
	}
	// Drop this file's own prior records up front; everything still valid is
	// rewritten below. Foreign-keyed records are left alone.
	ourPrefix := sourcePath + ":"
	for key := range state {
		if strings.HasPrefix(key, ourPrefix) {
			delete(state, key)
		}
	}

	// Sorted so the emitted records — and any warning — are stable across runs
	// rather than following Go's map order.
	events := make([]string, 0, len(hooks))
	for event := range hooks {
		if event != hookStateTable {
			events = append(events, event)
		}
	}
	sort.Strings(events)

	for _, event := range events {
		groups := asSlice(hooks[event])
		if len(groups) == 0 {
			continue
		}
		label, known := hookEventKeyLabels[event]
		if !known {
			unseedable = append(unseedable, fmt.Sprintf("[hooks.%s]: codex has no such hook event, so nothing under it can run", event))
			continue
		}
		for groupIndex, rawGroup := range groups {
			group := asMap(rawGroup)
			if group == nil {
				continue
			}
			matcher, hasMatcher := group["matcher"].(string)
			for handlerIndex, rawEntry := range asSlice(group["hooks"]) {
				entry := asMap(rawEntry)
				if entry == nil {
					continue
				}
				hash, err := hookIdentityHash(label, matcher, hasMatcher, entry)
				if err != nil {
					unseedable = append(unseedable, fmt.Sprintf("[hooks.%s] group %d hook %d: %v", event, groupIndex, handlerIndex, err))
					continue
				}
				state[hookStateKey(sourcePath, label, groupIndex, handlerIndex)] = map[string]any{
					"enabled":      true,
					"trusted_hash": hash,
				}
				seeded++
			}
		}
	}

	if len(state) == 0 {
		delete(hooks, hookStateTable)
		return seeded, unseedable
	}
	hooks[hookStateTable] = state
	return seeded, unseedable
}

// removeHookTrust drops the trust records THIS config.toml's own hooks were
// seeded with, pruning the [hooks.state] table (and, if it empties [hooks],
// that too) — the revert half of seedHookTrust, called from removeSettingsIn.
//
// Keyed records belonging to any other source are left exactly where they are,
// for the same reason removeManagedHooks only removes ctxloom's own entries: an
// uninstall that reaches past what it installed is a worse bug than one that
// leaves something behind.
func removeHookTrust(cfg map[string]any, sourcePath string) {
	hooks := asMap(cfg["hooks"])
	state := asMap(hooks[hookStateTable])
	if state == nil {
		return
	}
	ourPrefix := sourcePath + ":"
	for key := range state {
		if strings.HasPrefix(key, ourPrefix) {
			delete(state, key)
		}
	}
	if len(state) > 0 {
		return
	}
	delete(hooks, hookStateTable)
	if len(hooks) == 0 {
		delete(cfg, "hooks")
	}
}

// warnHooksWillNotRun is the fail-loud sibling of the seed: hooks that reached
// config.toml but will not run, said once, naming each one.
//
// A hook ctxloom wrote and codex skips is invisible from the outside — the run
// still exits 0 and the session still works, it just quietly has no hooks. That
// is exactly the shape this codebase refuses to ship, so both ways of arriving
// at it speak:
//
//   - reasons non-empty: individual hooks whose trust record could not be
//     computed (an unrecognized handler shape) or whose event codex does not
//     know. The rest of the file was seeded normally.
//   - seeding skipped entirely (trustAbsPath empty, i.e. NOT a ctxloom-owned
//     home): every hook in the file is untrusted, so none of them run under
//     `codex exec`. Today deliveryHome refuses every write on that axis before
//     reaching here, so this arm is a guard against a future caller that does
//     not — not dead code, but not a path a user hits now.
func warnHooksWillNotRun(settingsPath string, reasons []string) {
	clidiag.Warn("ctxloom", "codex will SKIP %d hook(s) written to %s: codex runs a hook only once its command is recorded as trusted (`[hooks.state]`), and under `codex exec` there is nobody to prompt — the run will succeed with those hooks silently not firing:\n  - %s",
		len(reasons), settingsPath, strings.Join(reasons, "\n  - "))
}

// warnHookTrustUnseeded is the whole-file arm of the same warning. See
// warnHooksWillNotRun.
func warnHookTrustUnseeded(settingsPath string, count int) {
	clidiag.Warn("ctxloom", "codex will SKIP all %d hook(s) written to %s: this is not a codex home ctxloom provisioned, so ctxloom does not record hook trust in it, and codex runs no hook it has not been told to trust. Declare `config_home: project` on this agent's binding to get a per-session codex home whose hooks ctxloom can vouch for.",
		count, settingsPath)
}

// countConfiguredHooks counts the command hooks in cfg's [hooks] table, for the
// unseeded warning's "all N hooks" — a warning that cannot name a number is one
// more thing the reader has to go and check.
func countConfiguredHooks(cfg map[string]any) int {
	hooks := asMap(cfg["hooks"])
	total := 0
	for event, groupsRaw := range hooks {
		if event == hookStateTable {
			continue
		}
		for _, rawGroup := range asSlice(groupsRaw) {
			total += len(asSlice(asMap(rawGroup)["hooks"]))
		}
	}
	return total
}

// --- canonical JSON --------------------------------------------------------
//
// version_for_toml hashes `serde_json::to_vec` of a key-sorted value: compact
// (no spaces), object keys in byte order. Only the handful of shapes the hook
// identity uses are needed, so they are emitted directly rather than via
// encoding/json — whose default HTML escaping (< for `<`, live in any hook
// command with a redirect) and its U+2028/U+2029 escaping both differ from
// serde_json and would silently produce a hash codex never matches.

// jsonField is one object member, already-encoded value alongside its key.
type jsonField struct {
	key   string
	value string
}

// canonicalObject encodes fields as a compact JSON object with keys in byte
// order — canonical_json's sort, which is Rust's String Ord.
func canonicalObject(fields []jsonField) string {
	sorted := make([]jsonField, len(fields))
	copy(sorted, fields)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].key < sorted[j].key })

	var b strings.Builder
	b.WriteByte('{')
	for i, f := range sorted {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jsonString(f.key))
		b.WriteByte(':')
		b.WriteString(f.value)
	}
	b.WriteByte('}')
	return b.String()
}

func jsonBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// jsonString encodes s exactly as serde_json does: the two mandatory escapes,
// the five short control escapes, \u00XX for every other C0 byte, and
// everything else — including DEL, U+2028 and all other non-ASCII — verbatim
// UTF-8.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
