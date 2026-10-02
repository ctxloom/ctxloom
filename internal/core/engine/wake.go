package engine

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// A WAKE starts a turn in an idle session so that session's turn-start hook
// runs and hands it its mail. HOW a turn can be started from outside is the
// engine's own fact, so each engine declares a WakeSpec (Engine.Wake) and
// brings its own implementation; nothing about the mechanism is shared
// between engines. Everything that does not depend on the engine — whether
// mail is pending, arming the nonce, the alarm when a wake goes unanswered,
// redeeming the nonce — is the caller's, not the Wake's.

// Wake is one session's bound wake. Fire delivers WakeText(nonce) as the
// prompt of a new turn, or refuses with a reason and delivers nothing.
type Wake interface {
	Fire(ctx context.Context, nonce string) error
}

// WakeSpec is an engine's declared way to wake a session. Bind fails — at
// bind time, naming the missing variable — when the environment lacks what
// the spec needs; nothing substitutes another spec at fire time.
type WakeSpec interface {
	Bind(ctx context.Context, env WakeEnv) (Wake, error)
}

// WakeEnv reads the environment the ENGINE handed out, which is the only
// place a wake can bind from, because what makes a wake land is
// engine-specific standing that only the engine can grant. Claude's is the
// environment it exports to the stdio servers it spawns: it delivers a post
// as its own session's only when the poster is its descendant holding that,
// so claude's spec binds in its relay, from the relay's own environment. An
// engine that names its wake in the exec env it composes for its session
// (the mock's socket) is bound by the runner from that env. A spec that finds
// nothing it can bind to returns ErrWakeUnbound.
type WakeEnv func(key string) (string, bool)

// WakeURI is the session's wake channel on its ctxloom MCP endpoint: an
// unlisted resource an engine's own session relay subscribes to. A wake is a
// resources/updated notification on it carrying the nonce in _meta. It is
// never listed, so the model never sees it: a control channel, not context.
const WakeURI = "ctxloom://session/wake"

// ErrWakeUnbound refuses a bind whose environment lacks what the spec needs.
var ErrWakeUnbound = errors.New("engine: the environment offers nothing this wake can bind to")

// wakePrefix and wakeSuffix frame the nonce in WakeText; wakeTextRE is their
// inverse. One renderer and one parser, adjacent, so they cannot disagree.
// The nonce grammar here is the one spool.ArmWake mints; the spool re-checks
// it before a nonce is ever joined to a path.
const (
	wakePrefix = "ctxloom: wake "
	wakeSuffix = " (mail delivered at turn start)"
)

var wakeTextRE = regexp.MustCompile(`^` + regexp.QuoteMeta(wakePrefix) + `([0-9a-f]{16})` + regexp.QuoteMeta(wakeSuffix) + `$`)

// WakeText is the text a wake delivers as the prompt of the turn it starts:
// a pure trigger. The mail itself is delivered by the turn-start hook.
func WakeText(nonce string) string { return wakePrefix + nonce + wakeSuffix }

// WakeNonce reports the nonce when prompt IS a wake — the whole prompt, not a
// prompt that mentions one. The distinction is load-bearing: the hook may
// block a wake that found no mail, and blocking a human's prompt that merely
// quoted the wake text would erase what they typed.
func WakeNonce(prompt string) (string, bool) {
	m := wakeTextRE.FindStringSubmatch(strings.TrimSpace(prompt))
	if m == nil {
		return "", false
	}
	return m[1], true
}
