package agent

import "strings"

// ThinkingLevel is the normalized, engine-agnostic reasoning/thinking-budget
// knob ctxloom hands each backend's Chat path. It is DELIBERATELY a
// four-tier qualitative enum, NOT a raw token count: Anthropic removed the
// token-count `budget_tokens` parameter from the Messages API for ctxloom's
// current default models (claude-opus-4-8, claude-sonnet-5 — sending it now
// returns 400) and now directs callers to a qualitative effort tier instead,
// for exactly the reason ctxloom cares about — a token means a different
// amount of "thinking" at different price points, and thinking tokens bill
// as OUTPUT tokens at the responding model's rate (a ~5x swing across
// haiku/sonnet/opus at the same nominal budget).
//
// Each backend that has a real mechanism translates this same vocabulary to
// its own knob (internal/claude/chat.go: MAX_THINKING_TOKENS env var;
// internal/codex/chat.go: model_reasoning_effort config key). A backend with
// no mechanism (kiro, opencode — verified by reading their chat.go) treats an
// explicit setting as a documented, WARNED no-op rather than a silent
// swallow — see their Configure methods.
type ThinkingLevel int

const (
	// ThinkingMedium is the ZERO VALUE and the DEFAULT every backend falls
	// back to when the labeled LLM config leaves `thinking` unset, sets an
	// unrecognized value, or never runs Configure at all — the "think hard"
	// tier, verified live to actually produce thought chunks (see
	// internal/claude/chat.go's doc comment). Deliberately ordered first so
	// an un-configured ThinkingLevel field defaults SAFELY to medium rather
	// than to an implicit "off" a plain int zero value would otherwise mean.
	ThinkingMedium ThinkingLevel = iota
	// ThinkingOff requests no extended thinking/reasoning at all.
	ThinkingOff
	// ThinkingLow is the cheapest non-zero tier.
	ThinkingLow
	// ThinkingHigh is the most expensive tier this vocabulary names. Not
	// every backend has a bare "high" of its own (codex's real values are
	// minimal/low/medium/xhigh — no bare high) — the per-backend mapping
	// decides which of ITS tiers this normalized level lands on.
	ThinkingHigh
)

// thinkingSpellings is the SINGLE source of the four canonical config
// spellings, listed in the order flag help and docs present them
// (cheapest-first) — deliberately NOT iota order, which starts at medium so an
// un-configured field defaults safely. String, ParseThinkingLevel and
// ThinkingLevelNames all read this table, so a fifth tier is one line and
// cannot land in some of the three and not the others (U102-F12: the four
// spellings used to be written out three times in this one file, and a tier
// added to the parser but not to the names list would parse, print, and never
// appear in flag help). Held by TestThinkingLevel_NamesCoverEveryDeclaredLevel.
var thinkingSpellings = []struct {
	level ThinkingLevel
	name  string
}{
	{ThinkingOff, "off"},
	{ThinkingLow, "low"},
	{ThinkingMedium, "medium"},
	{ThinkingHigh, "high"},
}

// String renders the canonical wire/config spelling. A value outside the
// declared tiers renders as the documented medium default.
func (l ThinkingLevel) String() string {
	for _, s := range thinkingSpellings {
		if s.level == l {
			return s.name
		}
	}
	return "medium"
}

// ParseThinkingLevel maps a config string to a ThinkingLevel. Lenient on
// case/whitespace. An empty or unrecognized value returns (ThinkingMedium,
// false) so callers can tell "unset" (silently apply the documented medium
// default) apart from "explicit but unrecognized" (warn, then still apply
// the default) — mirroring ParsePermissionMode's ok-bool contract.
func ParseThinkingLevel(s string) (ThinkingLevel, bool) {
	want := strings.ToLower(strings.TrimSpace(s))
	if want == "" {
		return ThinkingMedium, false
	}
	for _, sp := range thinkingSpellings {
		if sp.name == want {
			return sp.level, true
		}
	}
	return ThinkingMedium, false
}

// ThinkingLevelNames lists the accepted config values, for flag help/docs.
func ThinkingLevelNames() []string {
	names := make([]string, 0, len(thinkingSpellings))
	for _, s := range thinkingSpellings {
		names = append(names, s.name)
	}
	return names
}
