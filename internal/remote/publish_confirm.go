package remote

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/admission"
)

// The publish-remote confirmation: the first publish to a given remote URL
// asks a human and RECORDS the answer; every later publish to that same remote
// is silent. A session with nobody to ask REFUSES.
//
// WHAT THIS IS FOR, stated honestly. Publishing is intentionally outward and
// the content is signed to be verified by strangers, so this is
// MISTAKE-PREVENTION, not exfiltration defence — anyone who can run the
// publish can also run `git push` directly. It guards three things:
//
//  1. a typo or a stale remote in config. A typo produces a DIFFERENT URL and
//     therefore prompts; a routine republish to the same URL never does.
//  2. a committable .ctxloom/ config carrying a remote the user never chose —
//     a cloned repo can bring one.
//  3. an AGENT publishing. This project already treats that as needing a human
//     (ltk blocks `git tag` and release commands outright), and generic-git
//     publish is what makes pushing signed content reachable from inside an
//     agent session at all.
//
// It is the same trust-on-first-use decision the companion exec gate makes —
// ask once per new thing, remember, refuse rather than assume when nobody can
// be asked — and it is now literally the same code: internal/shared/admission.
// What stays here is what is about PUBLISHING: which remotes are gated at all,
// what makes two spellings of a URL one destination, and the refusal sentence.
//
// TWO ALTERNATIVES WERE REJECTED. Printing the destination and proceeding
// leaves an agent free to publish and trains people to skim routine output.
// An always-confirm with a `--yes` escape hatch puts the flag into every
// script on day one and makes the prompt reflexive.

// PublishAdmissionReason names WHY a publish destination was or was not
// admitted.
//
// The gate had no reason vocabulary at all before — a bool and an error — and
// that absence is what left it with no way to say "this was declined" as
// distinct from "nobody could be asked", and no way for a `list` surface to
// report what it found. Every other admission decision in ctxloom names its
// rule; this one now does too.
type PublishAdmissionReason string

const (
	// PublishAdmissionForgeToken: a GitHub remote, reached through the forge
	// API with a token that must already carry push rights to that specific
	// repository. Not gated here — see authorizeRemote for why widening the
	// gate to GitHub is a separate decision.
	PublishAdmissionForgeToken PublishAdmissionReason = "forge-token"
	// PublishAdmissionConfirmed: a recorded confirmation covers this remote.
	PublishAdmissionConfirmed PublishAdmissionReason = "confirmed"
	// PublishAdmissionDeclined: a recorded DECLINE covers this remote. A human
	// said no, and that survives until they forget it.
	PublishAdmissionDeclined PublishAdmissionReason = "declined"
	// PublishAdmissionUnconfirmed: never confirmed, and nothing could ask — a
	// non-interactive session, or a caller that does not prompt. Fail-closed,
	// and deliberately NOT the same answer as declined: the fix is a terminal
	// or `ctxloom trust publish allow`, not a change of mind.
	PublishAdmissionUnconfirmed PublishAdmissionReason = "unconfirmed"
	// PublishAdmissionStoreFault: the confirmation record exists but cannot be
	// read. Refuses, because "I could not read the store" must never read as
	// "you never confirmed it" — that asks a human to re-confirm a remote they
	// already approved, teaching them to say yes on reflex.
	PublishAdmissionStoreFault PublishAdmissionReason = "store-fault"
)

// PublishRemoteKey identifies one confirmed publish destination.
//
// It is also the whole record body on disk, so the file can be read, audited
// and pruned with `cat`. Only Identity is the key: the same repository reached
// as "git@host:o/r.git" and "https://host/o/r" is ONE destination, the same
// identity rule that keys the trust namespace and lockfile entries. URL is the
// spelling the human typed, kept for display and never decided on.
type PublishRemoteKey struct {
	// URL is the remote as written, for humans reading the record.
	URL string `yaml:"url"`
	// Identity is NormalizeURL(URL) — what two spellings of one repository
	// agree on, and the whole key.
	Identity string `yaml:"identity"`
}

// NewPublishRemoteKey builds the key for a remote URL. A URL that normalizes
// to nothing (an unparseable spelling) falls back to its raw text, which cannot
// silently collide two different destinations onto one confirmation.
func NewPublishRemoteKey(remoteURL string) PublishRemoteKey {
	id := NormalizeURL(remoteURL)
	if id == "" {
		id = strings.TrimSpace(remoteURL)
	}
	return PublishRemoteKey{URL: strings.TrimSpace(remoteURL), Identity: id}
}

// publishRemoteIdentity is the key AND the scope: a publish destination has
// nothing finer than itself, so an approval and a denial cover exactly the
// same thing and the store's scope/key asymmetry collapses to nothing here.
func publishRemoteIdentity(k PublishRemoteKey) string { return k.Identity }

// PublishRemoteRecord is one recorded decision about a publish destination.
//
// Nothing here is signed, so anything that can write the user's home can forge
// one. That is the documented cost of a decision record that must work with no
// key present — and it is exactly why the store is user-scoped
// (paths.HomePublishRemotesPath) and has no committable twin. A shareable
// "yes, publish there" with no signature would pre-answer the question for
// everyone who clones the repo, which is one of the three mistakes this
// mechanism exists to catch.
type PublishRemoteRecord = admission.Record[PublishRemoteKey]

// PublishRemoteStore is the personal record of confirmed publish destinations.
type PublishRemoteStore = admission.Store[PublishRemoteKey, PublishAdmissionReason]

// PublishRemoteAsk asks a human whether publishing to a remote nothing has
// been published to before may proceed.
//
// A nil PublishRemoteAsk is the NON-INTERACTIVE case and is the deliberate
// default: package remote has no terminal and must not acquire one. The
// frontend supplies this only when it actually has a human attached (see
// internal/cli), so an agent, an MCP tool call, a CI job and a piped
// invocation all reach the refusal rather than a prompt written into a pipe.
type PublishRemoteAsk = admission.Ask[PublishRemoteKey]

// publishRemoteReasons is this domain's half of the shared flow's vocabulary.
func publishRemoteReasons() admission.Reasons[PublishAdmissionReason] {
	return admission.Reasons[PublishAdmissionReason]{
		Approved: PublishAdmissionConfirmed,
		Declined: PublishAdmissionDeclined,
		Unasked:  PublishAdmissionUnconfirmed,
		Fault:    PublishAdmissionStoreFault,
	}
}

// NewPublishRemoteStore builds a store over path, backed by fs. Tests point it
// at a temp file instead of the user's home.
//
// An EMPTY path is a store nobody managed to configure (the real trigger is an
// unresolvable $HOME) and the shared store refuses every read and write in
// that state — see admission.NewStore for why that is a fault rather than an
// empty store.
func NewPublishRemoteStore(path string, fs afero.Fs) *PublishRemoteStore {
	return admission.NewStore(fs, path, publishRemoteIdentity, publishRemoteReasons())
}

// DefaultPublishRemoteStore is the production store, at
// ~/.ctxloom/publish_remotes.yaml. An unresolvable home yields an UNCONFIGURED
// store rather than an error, so construction stays total; the fault surfaces
// at the gate, where it fails closed.
//
// This is also the narrowest point at which publish consent is actually
// consulted — every caller (list/allow/deny/forget and the publish gate
// itself) routes through here, and nothing at unconditional startup does —
// so it is where the orphaned legacy marker directory
// (paths.LegacyPublishRemotesDirName) gets swept: a session that never
// touches publish never pays for it. See sweepLegacyPublishRemotesDir for the
// safety discipline that guards the deletion.
func DefaultPublishRemoteStore() *PublishRemoteStore {
	sweepLegacyPublishRemotesDir()
	path, err := paths.HomePublishRemotesPath()
	if err != nil {
		path = ""
	}
	return NewPublishRemoteStore(path, afero.NewOsFs())
}

// ListPublishRemoteConsent returns every recorded decision about a publish
// destination, sorted by identity. The read half of `ctxloom trust publish
// list`.
func ListPublishRemoteConsent() ([]PublishRemoteRecord, error) {
	return DefaultPublishRemoteStore().List()
}

// SetPublishRemoteConsent records an explicit decision about remoteURL. It is
// the scriptable form of the interactive prompt — the escape hatch a CI job or
// an agent host needs, deliberately requiring a human to type it rather than
// inferring consent from the environment.
//
// Before it existed, a non-interactive caller was told to "run the same
// publish once from an interactive terminal", which a CI runner and an agent
// host cannot do at all.
func SetPublishRemoteConsent(remoteURL string, approved bool) (PublishRemoteRecord, error) {
	var zero PublishRemoteRecord
	if strings.TrimSpace(remoteURL) == "" {
		return zero, fmt.Errorf("publish-remote consent: no remote named")
	}
	return DefaultPublishRemoteStore().Set(NewPublishRemoteKey(remoteURL), approved)
}

// ForgetPublishRemoteConsent drops the recorded decision for remoteURL and
// reports how many entries were removed. Zero removed is reported as zero,
// never as success-with-no-effect: forgetting a remote nobody recorded is the
// caller's mistake to see.
func ForgetPublishRemoteConsent(remoteURL string) (int, error) {
	if strings.TrimSpace(remoteURL) == "" {
		return 0, fmt.Errorf("publish-remote consent: no remote named")
	}
	return DefaultPublishRemoteStore().Forget(NewPublishRemoteKey(remoteURL))
}

// PublishRemoteConsentPath reports the file confirmations live in, for
// messages that must tell a human where the answer was (or would be) recorded.
func PublishRemoteConsentPath() string {
	path, err := paths.HomePublishRemotesPath()
	if err != nil {
		return ""
	}
	return path
}

// authorizeRemote is the gate every non-GitHub publish passes before any
// network write.
//
// SCOPE: generic-git remotes only. GitHub publish is deliberately untouched —
// it goes through the forge API with a token that must already carry push
// rights to that specific repository, and folding it in here would change the
// behaviour of an existing, acceptance-covered path as a side effect of adding
// a new one. Widening the gate to GitHub is a separate decision with its own
// migration for everyone already publishing there.
func (pm *PublishManager) authorizeRemote(ctx context.Context, remoteURL string, forge ForgeType) error {
	store := pm.publishRemoteStore()
	d := pm.admitRemote(ctx, store, remoteURL, forge)
	if d.Allow {
		return nil
	}
	return publishRefusedError(remoteURL, d, store.Path())
}

// publishRemoteStore is the store this manager decides against: the one an
// option supplied, or the production one.
func (pm *PublishManager) publishRemoteStore() *PublishRemoteStore {
	if pm != nil && pm.confirmed != nil {
		return pm.confirmed
	}
	return DefaultPublishRemoteStore()
}

// admitRemote is the decision itself, as a Decision rather than as an error,
// so the gate and any surface that has to REPORT what it would decide share
// one vocabulary instead of re-deriving it from an error string.
//
// It is a pure function of its inputs plus the store: it renders nothing. The
// sentence a human reads is assembled by publishRefusedError from the Reason.
func (pm *PublishManager) admitRemote(
	ctx context.Context, store *PublishRemoteStore, remoteURL string, forge ForgeType,
) admission.Decision[PublishAdmissionReason] {
	if forge == ForgeGitHub {
		return admission.Decision[PublishAdmissionReason]{Allow: true, Reason: PublishAdmissionForgeToken}
	}
	// Recorded BEFORE publishing, by Decide. A confirmation only written after
	// a successful push is lost whenever the push fails, so the retry asks
	// again — and a prompt that reappears after every hiccup is one people
	// learn to dismiss. The human answered the question that was asked ("is
	// this the remote you meant"), and that answer is true regardless of
	// whether the push then succeeded.
	d, err := store.Decide(ctx, NewPublishRemoteKey(remoteURL), pm.ask)
	if err != nil && d.Allow {
		// Confirmed, but the confirmation could not be recorded. Unlike the
		// companion gate — which honors the yes and warns — this one REFUSES:
		// a confirmation that did not persist means the prompt reappears on
		// every retry, which is exactly the reflex-training this mechanism
		// exists to avoid.
		return admission.Decision[PublishAdmissionReason]{
			Reason: PublishAdmissionStoreFault,
			Detail: fmt.Sprintf("it was confirmed but the confirmation could not be recorded "+
				"(it would be asked again every time): %v", err),
		}
	}
	return d
}

// publishRefusedError turns a refusal into the sentence a human reads. It
// names the remote, says which rule stopped it, and says how to proceed —
// including, for the non-interactive case, a command a CI job or an agent host
// can actually run, which "run it once interactively" was not.
func publishRefusedError(remoteURL string, d admission.Decision[PublishAdmissionReason], storePath string) error {
	where := storePath
	if where == "" {
		where = "~/" + paths.AppDirName + "/" + paths.PublishRemotesFileName + ".yaml"
	}
	switch d.Reason {
	case PublishAdmissionDeclined:
		return fmt.Errorf(
			"refusing to publish to %s: it is recorded as declined as a publish destination "+
				"(undo with 'ctxloom trust publish forget %s')", remoteURL, remoteURL)
	case PublishAdmissionStoreFault:
		detail := d.Detail
		if detail == "" {
			detail = "the confirmation record could not be read"
		}
		return fmt.Errorf(
			"refusing to publish to %s: cannot tell whether it is a confirmed publish remote: %s",
			remoteURL, detail)
	default:
		// PublishAdmissionUnconfirmed, and the fail-closed default for any
		// reason added without a case here.
		detail := ""
		if d.Detail != "" {
			detail = " (" + d.Detail + ")"
		}
		return fmt.Errorf(
			"refusing to publish to %s: this remote has never been confirmed as a publish destination, "+
				"and this session has no terminal to confirm it on (an agent, an editor, a CI job or a piped command)%s. "+
				"Check the URL is the one you meant, then run 'ctxloom trust publish allow %s'; "+
				"the answer is recorded in %s and you will not be asked again",
			remoteURL, detail, remoteURL, where)
	}
}
