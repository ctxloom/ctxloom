// Package agentkey implements the zero-config signing-key discovery chain
// (signature-envelope spec §7A.4): the resolution ctxloom sign and the
// --sign publish flags use to find WHO is signing, without asking the user
// to configure anything ctxloom-specific.
//
// The chain, in evaluation order:
//
//  1. An explicit key (--key, or the sign.key config value) wins outright
//     when given.
//  2. `git config user.signingkey` — the zero-config path. Anyone already
//     signing git commits with SSH has this set, and expects tools to find
//     it without being told.
//  3. The sole identity held by ssh-agent, when there is exactly one.
//
// ctxloom never generates, stores, reads, or prompts for private key
// material (spec §9.1) — every Discovered.Signer returned here is backed by
// a live ssh-agent connection (golang.org/x/crypto/ssh/agent), never a file
// on disk. Discover deliberately never guesses: an ambiguous chain (agent
// holds several identities, nothing narrowed the choice) and an empty chain
// (no key anywhere) both return a distinguishable, actionable error rather
// than picking one — signing under the wrong identity produces a signature
// nobody trusts, and the publisher will not find out until a user
// complains.
//
// The trust boundary on step 2, stated rather than assumed: execGitConfig runs
// `git config --get user.signingkey` with cmd.Dir set to the working
// repository, so git answers from that repository's own .git/config — a file
// that arrives with a clone. A repository you clone can therefore influence
// WHICH OF YOUR CONTROLLED KEYS signs. It cannot cause an attestation from a
// key you do not hold: every signer here is a live ssh-agent identity, and a
// key the attacker holds is not in your agent. A ctxloom signature is an
// attestation from a controlled key/identity, and so is a git signature — `git
// commit -S` resolves user.signingkey out of the same file and asserts the same
// kind of property — which is why inheriting git's resolution boundary is
// principled rather than convention-following. The residual is git's residual:
// the attestation still comes from an identity you control, but possibly a
// DIFFERENT one than intended (a personal key where a work key was meant),
// which is an attribution problem, not a break of the attestation property.
// Ruled and accepted 2026-07-31, and pinned by
// TestGitSigningKey_RepoLocalConfigNamesTheFileRead. Two alternatives were
// rejected and should not be re-litigated: restricting the lookup to
// global/system config (it breaks per-repository identities, a normal and
// legitimate setup, and guts step 2 of this documented chain), and prompting
// for confirmation when the value came from repo-local config (it adds a
// consent surface to a flow that most often runs unattended in CI).
//
// This package is the shared seam between the publisher-signing slice
// (ctxloom sign, --sign) and the countersigning slice (ctxloom review):
// both need "which key should I sign with right now", and this is the one
// place that question is answered.
package agentkey

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// withheldKeyValue is what a key value is replaced with when it must not be
// printed. It still reports the length, which is the only part that helps
// someone diagnose a mis-paste.
const withheldKeyValue = "<redacted: %d bytes of key material>"

// mustWithhold reports whether a --key / sign.key / user.signingkey value must
// never be echoed back to the user.
//
// The invariant: ctxloom does not read private key material (package doc, spec
// §9.1), and it must not PRINT it either. Errors are the one place a value the
// user typed comes back out, and they are exactly what gets pasted into bug
// reports, CI logs and chat. A value carrying a private-key header, or one
// spanning lines, is not any of the things this package accepts — a SHA256
// fingerprint, a filesystem path, a single authorized-keys line, or an
// ssh-agent comment are all one line and none says PRIVATE KEY — so nothing
// legitimate is ever withheld by this rule. A trailing newline alone does not
// trip it: `--key "$(cat id.pub)"` is a normal thing to type.
func mustWithhold(value string) bool {
	return strings.Contains(value, "PRIVATE KEY") ||
		strings.ContainsAny(strings.TrimSpace(value), "\n\r")
}

// displayKeyValue renders a key value for an error message: quoted verbatim
// when it is safe, withheld when it is not. Naming the value back is the whole
// worth of these errors, so the safe case must stay verbatim.
func displayKeyValue(value string) string {
	if mustWithhold(value) {
		return fmt.Sprintf(withheldKeyValue, len(value))
	}
	return fmt.Sprintf("%q", value)
}

// Discovered is the resolved signing identity.
type Discovered struct {
	// Signer signs over the ssh-agent connection. Never backed by key
	// material ctxloom read itself.
	Signer ssh.Signer
	// Fingerprint is ssh.FingerprintSHA256(Signer.PublicKey()), cached for
	// display.
	Fingerprint string
	// Source describes where this identity came from, for status output:
	// "git config user.signingkey", "ssh-agent (sole identity)", "--key",
	// or "sign.key config".
	Source string

	// closer owns the transport Signer speaks over, when this package opened
	// it. Unexported: the connection's lifetime is Close's business, not a
	// field a caller should reach into.
	closer io.Closer
}

// Close releases the ssh-agent connection backing Signer. Signer must not be
// used afterwards.
//
// A resolution that opened a connection hands its ownership to the caller,
// because Signer signs over it and therefore has to outlive Discover. Close is
// safe on a nil Discovered, on one whose agent connection this package did not
// open, and when called more than once — so every caller can defer it
// unconditionally.
func (d *Discovered) Close() error {
	if d == nil || d.closer == nil {
		return nil
	}
	c := d.closer
	d.closer = nil
	return c.Close()
}

// Candidate is one ssh-agent identity, surfaced in an ambiguous-choice
// error so the user can pick by fingerprint.
type Candidate struct {
	Fingerprint string
	Type        string
	Comment     string
}

// AmbiguousKeyError reports that ssh-agent holds more than one identity and
// nothing (git config, --key, sign.key) narrowed the choice (spec §7A.4,
// "Ambiguous"). This is a hard error by design: Discover never guesses.
type AmbiguousKeyError struct {
	Candidates []Candidate
}

// AmbiguousKeyHeader names the event "ssh-agent holds several identities and
// nothing chose between them". Exported so a surface that wraps the listing in
// its own prose still describes the same event in the same words.
const AmbiguousKeyHeader = "ctxloom: multiple keys in ssh-agent — which should sign?"

// candidateLines renders the ambiguous-choice listing: one line per identity,
// fingerprint, key type, and the agent comment when there is one.
//
// The comment is the IDENTIFYING part. Two ed25519 keys are otherwise
// indistinguishable to the human being asked to choose, and the comment is
// what they must then type. A surface that re-renders the candidates by hand
// loses precisely what makes the listing worth printing.
func candidateLines(candidates []Candidate) []string {
	lines := make([]string, 0, len(candidates))
	for _, c := range candidates {
		line := fmt.Sprintf("  %s  %s", c.Fingerprint, c.Type)
		if c.Comment != "" {
			line += fmt.Sprintf("  (%s)", c.Comment)
		}
		lines = append(lines, line)
	}
	return lines
}

// CandidateLines is the one authoring of the ambiguous-choice listing, for
// callers that supply their own surrounding prose. See candidateLines for why
// re-rendering it by hand is a mistake.
func (e *AmbiguousKeyError) CandidateLines() []string { return candidateLines(e.Candidates) }

func (e *AmbiguousKeyError) Error() string {
	var b strings.Builder
	b.WriteString(AmbiguousKeyHeader + "\n\n")
	for _, line := range e.CandidateLines() {
		b.WriteString(line + "\n")
	}
	b.WriteString("\nPick one, and make it stick:\n")
	if len(e.Candidates) > 0 {
		first := e.Candidates[0]
		// Prefer the name (the agent comment, e.g. "ben@abbitt.me") when
		// there is one — it's what a human actually recognizes, and it's
		// right there in the listing above. A comment can be empty (not
		// every ssh-add sets one), so the fingerprint stays the fallback.
		pick := first.Comment
		if pick == "" {
			pick = first.Fingerprint
		}
		fmt.Fprintf(&b, "  ctxloom config set sign.key %s\n", pick)
	}
	b.WriteString("  git config gpg.format ssh && git config user.signingkey ~/.ssh/id_ed25519.pub\n")
	return b.String()
}

// AmbiguousKeyNameError reports that an explicit --key/sign.key NAME (a
// case-insensitive substring match against ssh-agent key comments — the
// last resort of resolveExplicit's fallback chain) matched more than one
// agent identity. Like AmbiguousKeyError, this is a hard error by design:
// Discover never guesses between candidates.
type AmbiguousKeyNameError struct {
	// Name is the --key/sign.key value that was matched against comments.
	Name string
	// Candidates are every agent identity whose comment matched Name.
	Candidates []Candidate
}

// CandidateLines is the one authoring of the ambiguous-choice listing, shared
// with AmbiguousKeyError: the two describe different reasons for the same
// situation, and must describe the candidates identically.
func (e *AmbiguousKeyNameError) CandidateLines() []string { return candidateLines(e.Candidates) }

func (e *AmbiguousKeyNameError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ctxloom: --key %s matches %d keys in ssh-agent.\n\n", displayKeyValue(e.Name), len(e.Candidates))
	for _, line := range e.CandidateLines() {
		b.WriteString(line + "\n")
	}
	b.WriteString("\nNarrow the name, or disambiguate with the fingerprint:\n")
	if len(e.Candidates) > 0 {
		fmt.Fprintf(&b, "  ctxloom bundle sign <bundle> --key %s\n", e.Candidates[0].Fingerprint)
	}
	return b.String()
}

// NoKeyError reports that no signing key was found anywhere in the
// resolution chain (spec §7A.4, "Empty"). Normative: a caller that receives
// this MUST treat it as a hard failure to sign, never degrade to an
// unsigned publish.
type NoKeyError struct {
	// Looked names the sources that were actually consulted, in order, for
	// the error message ("Looked for: ...").
	Looked []string
	// Detail is an optional root cause (e.g. "ssh-agent not running", "key
	// named by git config is not loaded"), for display.
	Detail string
	// Err is the underlying error Detail was derived from, when one exists:
	// an ssh-agent RPC/protocol failure (agent locked, a
	// wedged socket) is a DIFFERENT fact than "the agent is reachable but
	// does not hold this key", and a caller that wants to tell them apart —
	// or simply wants the real cause via errors.Is/As — must not find it
	// flattened into a string with no way back to the original error.
	Err error
}

// Unwrap exposes Err so errors.Is/errors.As can reach the underlying cause
// through a NoKeyError, rather than it being a dead end.
func (e *NoKeyError) Unwrap() error { return e.Err }

func (e *NoKeyError) Error() string {
	var b strings.Builder
	b.WriteString("ctxloom: cannot sign — no signing key found.\n\n")
	if len(e.Looked) > 0 {
		fmt.Fprintf(&b, "  Looked for: %s.\n", strings.Join(e.Looked, ", then "))
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, "  %s\n", e.Detail)
	}
	b.WriteString("\n  ssh-add ~/.ssh/id_ed25519            # load a key you already have\n")
	b.WriteString("  ssh-keygen -t ed25519-sk             # or a hardware key (recommended)\n\n")
	b.WriteString("Publishing unsigned means every user of this bundle must review it by hand.\n")
	b.WriteString("To publish unsigned anyway: ctxloom bundle push <bundle> --no-sign\n")
	return b.String()
}

// Discoverer resolves a signing identity via the chain documented on the
// package. Every field is overridable so Discover is exercisable without a
// real git binary, a real repository, or a real ssh-agent socket.
type Discoverer struct {
	// Dir is the working directory `git config` runs in. Empty uses the
	// process's own cwd.
	Dir string

	// GitConfig resolves a git config key, returning ("", false, nil) when
	// the key is unset. Defaults to shelling out to the git binary.
	GitConfig func(ctx context.Context, dir, key string) (value string, ok bool, err error)

	// DialAgent connects to the running ssh-agent. Defaults to dialing
	// SSH_AUTH_SOCK. Returns an error when no agent is reachable — that
	// error is surfaced (wrapped) in NoKeyError/AmbiguousKeyError paths,
	// never silently swallowed.
	DialAgent func() (agent.Agent, error)

	// ReadFile reads a file's bytes, used to resolve a git signingkey value
	// or an explicit --key value that names a path rather than a literal
	// key or a fingerprint. Defaults to os.ReadFile. ctxloom only ever
	// reads PUBLIC key material this way — see resolvePublicKey.
	ReadFile func(path string) ([]byte, error)
}

// The three accessors below are what make the fields' documented defaults true
// of the TYPE rather than only of NewDiscoverer. A Discoverer is routinely
// built as a struct literal overriding one field, which is exactly what "every
// field is overridable" invites; every read of a nil field must therefore
// resolve to the default the field's own doc names, never dereference nil.

func (d *Discoverer) gitConfig() func(ctx context.Context, dir, key string) (string, bool, error) {
	if d.GitConfig != nil {
		return d.GitConfig
	}
	return execGitConfig
}

func (d *Discoverer) dialAgent() (agent.Agent, error) {
	if d.DialAgent != nil {
		return d.DialAgent()
	}
	return dialEnvAgent()
}

// maxPublicKeyBytes bounds what will be read from a path named by a key value.
//
// That path is not necessarily one the user chose. Step 2 of the chain honours
// `git config user.signingkey`, and `git config --get` consults the
// REPOSITORY's .git/config, so cloning a repository is enough to name the file
// ctxloom opens. An unbounded read of a path someone else named is a hang
// waiting to happen: /dev/zero reports size 0 and never reaches EOF, so
// os.ReadFile grows its buffer until the process dies.
//
// Nothing legitimate comes close. The largest value that parses here is a
// single authorized-keys line, and an RSA-4096 entry with a comment is under
// 1 KiB.
const maxPublicKeyBytes = 64 << 10

// readPublicKeyFile is the default Discoverer.ReadFile. It reads one byte past
// the ceiling so an oversized file is REPORTED by readFile rather than
// silently truncated into an unparseable key.
func readPublicKeyFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxPublicKeyBytes+1))
}

func (d *Discoverer) readFile(path string) ([]byte, error) {
	read := readPublicKeyFile
	if d.ReadFile != nil {
		read = d.ReadFile
	}
	data, err := read(path)
	if err != nil {
		return nil, err
	}
	// The ceiling is enforced here so it holds for an injected ReadFile too.
	// Only the default can prevent the oversized read from happening at all;
	// an injected one has already returned the bytes by this point.
	if len(data) > maxPublicKeyBytes {
		return nil, fmt.Errorf("%s is over %d bytes — too large to be a public key file",
			displayKeyValue(path), maxPublicKeyBytes)
	}
	return data, nil
}

// NewDiscoverer returns a Discoverer wired to the real git binary and the
// real ssh-agent named by SSH_AUTH_SOCK — the production configuration. It is
// the explicit form of what a zero Discoverer already resolves to; callers
// that want to name the wiring, or to read a default out of a field, use it.
func NewDiscoverer() *Discoverer {
	return &Discoverer{
		GitConfig: execGitConfig,
		DialAgent: dialEnvAgent,
		ReadFile:  readPublicKeyFile,
	}
}

func execGitConfig(ctx context.Context, dir, key string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "git", "config", "--get", key)
	if dir != "" {
		cmd.Dir = dir
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && stderr.Len() == 0 {
			// `git config --get` exits 1 with no stderr when the key is
			// simply unset — not a failure, just "not configured".
			return "", false, nil
		}
		// git contributes stderr only when it actually ran. A failure on
		// ctxloom's side of exec (unrunnable binary, bad cwd) leaves it
		// empty, so the message must stand on its own rather than being
		// prefixed with nothing.
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", false, fmt.Errorf("git config --get %s: %s: %w", key, msg, err)
		}
		return "", false, fmt.Errorf("git config --get %s: %w", key, err)
	}
	value := strings.TrimSpace(out.String())
	return value, value != "", nil
}

func dialEnvAgent() (agent.Agent, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK is not set — no ssh-agent to sign with")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("connect to ssh-agent at %s: %w", sock, err)
	}
	return &closingAgent{ExtendedAgent: agent.NewClient(conn), conn: conn}, nil
}

// closingAgent is an agent.Agent that also owns the connection it speaks over.
//
// It exists because DialAgent's signature hands back only an agent.Agent, so
// there is nowhere else to put the connection: whoever dialled must be able to
// release it without the field's type changing under every caller.
type closingAgent struct {
	agent.ExtendedAgent
	conn net.Conn
}

func (a *closingAgent) Close() error { return a.conn.Close() }

// releaseUnlessRetained closes the agent connection unless the resolution kept
// it. Every resolver defers this: on any error arm the socket is released
// before the error leaves, and on success it is deliberately NOT — the
// Discovered that owns it signs over that connection, and closing early would
// break signing rather than fix a leak.
func releaseUnlessRetained(ag agent.Agent, result **Discovered) {
	if *result != nil {
		return
	}
	if c, ok := ag.(io.Closer); ok {
		_ = c.Close()
	}
}

// retain records the agent connection on a resolved identity, so Close can
// reach it and releaseUnlessRetained leaves it alone.
func retain(d *Discovered, ag agent.Agent) *Discovered {
	if d == nil {
		return d
	}
	if c, ok := ag.(io.Closer); ok {
		d.closer = c
	}
	return d
}

// Discover resolves a signing identity. explicitKey is the caller's
// already-merged --key/sign.key value (empty when neither was given); the
// caller is responsible for that precedence merge (an explicit flag beating
// a config default), Discover only knows "was one supplied or not".
func (d *Discoverer) Discover(ctx context.Context, explicitKey string) (*Discovered, error) {
	if explicitKey != "" {
		return d.resolveExplicit(ctx, explicitKey)
	}

	gitKey, ok, err := d.gitConfig()(ctx, d.Dir, "user.signingkey")
	if err != nil {
		return nil, fmt.Errorf("reading git config user.signingkey: %w", err)
	}
	if ok && gitKey != "" {
		return d.resolveGitSigningKey(gitKey)
	}

	return d.resolveSoleAgentIdentity([]string{"git config user.signingkey", "ssh-agent identities"})
}

// noKeyFrom classifies a non-ambiguous resolution failure.
//
// Discover fails in exactly two shapes, and which one a caller gets must
// depend on WHAT went wrong, never on which step of the chain was running:
// *AmbiguousKeyError / *AmbiguousKeyNameError mean candidates exist and the
// user must choose, and *NoKeyError means nothing usable resolved. NoKeyError
// tells callers they MUST treat it as a hard failure to sign, which is only
// actionable if the type covers every such failure. cause is kept both as the
// displayed Detail and, via Err, as something errors.Is/As can still reach.
func noKeyFrom(looked []string, cause error) *NoKeyError {
	return &NoKeyError{Looked: looked, Detail: cause.Error(), Err: cause}
}

// resolveExplicit resolves --key/sign.key in fallback order: (a) a SHA256
// fingerprint, (b) a key line/literal/path to a public key, (c) — only when
// (b) fails to produce a key at all — a case-insensitive substring match
// against a live ssh-agent identity's COMMENT (the name ctxloom itself
// prints in the ambiguous-key listing, e.g. "ben@abbitt.me"). (a) and (b)
// must keep priority over (c): a value that legitimately parses as a
// fingerprint or resolves as a file must never be reinterpreted as a name
// just because it happens to also resemble one.
func (d *Discoverer) resolveExplicit(ctx context.Context, explicitKey string) (result *Discovered, err error) {
	looked := []string{"--key/sign.key " + displayKeyValue(explicitKey)}

	ag, err := d.dialAgent()
	if err != nil {
		return nil, noKeyFrom(looked, err)
	}
	defer releaseUnlessRetained(ag, &result)

	if strings.HasPrefix(explicitKey, "SHA256:") {
		d2, ferr := findByFingerprint(ag, explicitKey, "--key")
		if ferr != nil {
			return nil, noKeyFrom(looked, ferr)
		}
		return retain(d2, ag), nil
	}

	pub, pubErr := d.resolvePublicKey(explicitKey)
	if pubErr == nil {
		d2, ferr := findByPublicKey(ag, pub, "--key")
		if ferr != nil {
			return nil, noKeyFrom(looked, ferr)
		}
		return retain(d2, ag), nil
	}

	discovered, nameErr := d.resolveByComment(ag, explicitKey)
	if nameErr == nil {
		return retain(discovered, ag), nil
	}
	var ambigName *AmbiguousKeyNameError
	if errors.As(nameErr, &ambigName) {
		// Ambiguity is a hard error — never guess, and never mask it behind
		// the earlier "not a recognized public key" message.
		return nil, nameErr
	}

	// nameErr is not just "no comment matched" — resolveByComment
	// also returns "listing ssh-agent identities: <rpc err>" when ag.Signers()
	// itself fails (agent locked, wedged socket). That used to be silently
	// dropped here in favor of pubErr alone, so an agent RPC failure was
	// reported as "not a recognized fingerprint, public key, or ssh-agent key
	// name" — true but misleading about WHY. Chain nameErr too.
	return nil, noKeyFrom(looked, fmt.Errorf("not a recognized fingerprint or public key (%v); and %w", pubErr, nameErr))
}

// resolveByComment is the last resort of resolveExplicit's fallback chain:
// match explicitKey as a case-insensitive substring against each ssh-agent
// identity's comment. A key with an empty comment is never matched — an
// unconditional substring check against "" would otherwise match every key,
// which defeats the whole point of naming one.
func (d *Discoverer) resolveByComment(ag agent.Agent, explicitKey string) (*Discovered, error) {
	needle := strings.ToLower(strings.TrimSpace(explicitKey))
	if needle == "" {
		return nil, fmt.Errorf("no key name given")
	}

	signers, err := ag.Signers()
	if err != nil {
		return nil, fmt.Errorf("listing ssh-agent identities: %w", err)
	}
	candidates := candidatesFromSigners(ag, signers)

	var matched []int
	for i, c := range candidates {
		if c.Comment == "" {
			continue
		}
		if strings.Contains(strings.ToLower(c.Comment), needle) {
			matched = append(matched, i)
		}
	}

	switch len(matched) {
	case 0:
		return nil, fmt.Errorf("no ssh-agent identity comment matches %s", displayKeyValue(explicitKey))
	case 1:
		s := signers[matched[0]]
		return &Discovered{
			Signer:      s,
			Fingerprint: candidates[matched[0]].Fingerprint,
			Source:      "--key",
		}, nil
	default:
		matches := make([]Candidate, 0, len(matched))
		for _, i := range matched {
			matches = append(matches, candidates[i])
		}
		return nil, &AmbiguousKeyNameError{Name: explicitKey, Candidates: matches}
	}
}

// resolveGitSigningKey resolves `git config user.signingkey`'s value (a
// literal "ssh-<type> AAAA..." string, a "key::<literal>" prefix per git
// 2.34+, or a path to a public key file) to a live ssh-agent identity.
// This used to take an unused context.Context — the actual
// blocking I/O (agent dial + RPC) has no cancellation seam at all, so the
// parameter carried no information and could mislead a caller into thinking
// ctx cancellation was honored here. Dropped rather than left lying; wiring
// real cancellation into DialAgent is a separate, larger change.
func (d *Discoverer) resolveGitSigningKey(value string) (result *Discovered, err error) {
	gitLooked := []string{"git config user.signingkey"}

	pub, err := d.resolvePublicKey(value)
	if err != nil {
		return nil, noKeyFrom(gitLooked, fmt.Errorf("git names %s, but %w", displayKeyValue(value), err))
	}

	ag, err := d.dialAgent()
	if err != nil {
		return nil, noKeyFrom(gitLooked, fmt.Errorf("git names %s, but %w", ssh.FingerprintSHA256(pub), err))
	}
	defer releaseUnlessRetained(ag, &result)

	d2, err := findByPublicKey(ag, pub, "git config user.signingkey")
	if err != nil {
		// findByPublicKey fails BOTH when the key is genuinely
		// absent from the agent AND when ag.Signers() itself errored (agent
		// locked, wedged socket, RPC failure) — those are different facts,
		// and the second one is not fixed by "ssh-add it": the key IS loaded,
		// the agent just could not be asked. Surface findByPublicKey's own
		// message (and chain its cause via Err) instead of guessing.
		return nil, noKeyFrom(gitLooked, fmt.Errorf("git names %s, but %w", ssh.FingerprintSHA256(pub), err))
	}
	return retain(d2, ag), nil
}

// resolveSoleAgentIdentity implements step 3 of the chain: use the agent's
// only identity when there is exactly one, error (ambiguous or empty)
// otherwise. ctx was accepted but never used; dropped (see resolveGitSigningKey).
func (d *Discoverer) resolveSoleAgentIdentity(looked []string) (result *Discovered, err error) {
	ag, err := d.dialAgent()
	if err != nil {
		return nil, noKeyFrom(looked, err)
	}
	defer releaseUnlessRetained(ag, &result)

	signers, err := ag.Signers()
	if err != nil {
		// An ssh-agent RPC failure while LISTING identities
		// is a different fact than "the agent holds no identities" — chain
		// the real cause via Err so a caller can errors.Is/As it, not just
		// read a flattened string.
		return nil, noKeyFrom(looked, fmt.Errorf("listing ssh-agent identities: %w", err))
	}
	if len(signers) == 0 {
		return nil, &NoKeyError{Looked: looked}
	}
	if len(signers) > 1 {
		return nil, &AmbiguousKeyError{Candidates: candidatesFromSigners(ag, signers)}
	}

	s := signers[0]
	return retain(&Discovered{
		Signer:      s,
		Fingerprint: ssh.FingerprintSHA256(s.PublicKey()),
		Source:      "ssh-agent (sole identity)",
	}, ag), nil
}

// resolvePublicKey turns a git-signingkey-shaped or --key-shaped value into
// an ssh.PublicKey. It reads only PUBLIC key material — a "key::<literal>"
// prefix or a literal "ssh-<type> AAAA..." authorized-keys line is parsed
// in-memory, and a path is read via d.ReadFile and parsed the same way.
// ctxloom never attempts to parse a value as a PRIVATE key (spec §9.1: it
// does not read private key material, ever).
func (d *Discoverer) resolvePublicKey(value string) (ssh.PublicKey, error) {
	literal := strings.TrimPrefix(value, "key::")
	if pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(literal)); err == nil {
		return pub, nil
	}

	// A value that must be withheld is not a path and must never be USED as
	// one. os.ReadFile reports the path it failed on, and that *fs.PathError
	// is wrapped into the message the user is about to read — so treating
	// pasted private key material as a filename is how the whole key ends up
	// on stderr, twice.
	if mustWithhold(value) {
		return nil, fmt.Errorf("%s is not a public key, and is not read as a file path", displayKeyValue(value))
	}

	path, err := expandHome(value)
	if err != nil {
		return nil, err
	}

	// user.signingkey conventionally names the PRIVATE key path in
	// some real setups, with the public key living alongside it as
	// "<path>.pub". Probe that sibling FIRST — before ever reading `path`
	// itself — whenever path doesn't already end in .pub. The old fallback
	// lived only inside the `ReadFile(path)` FAILURE branch, so it could
	// never fire for exactly the case the comment names: the private-key
	// path reads successfully (it exists), so ssh.ParseAuthorizedKey simply
	// failed on private-key bytes and the whole zero-config git path broke.
	// Trying the sibling first also means ctxloom never reads private key
	// bytes into memory at all when a .pub sibling exists — tightening the
	// package doc's "never reads private key material" claim rather than
	// merely making it true on the happy path.
	if !strings.HasSuffix(path, ".pub") {
		if data, err := d.readFile(path + ".pub"); err == nil {
			if pub, _, _, _, perr := ssh.ParseAuthorizedKey(data); perr == nil {
				return pub, nil
			}
		}
	}

	data, err := d.readFile(path)
	if err != nil {
		return nil, fmt.Errorf("not a recognized public key and unreadable as a file: %w", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return nil, fmt.Errorf("%s does not contain a parseable SSH public key: %w", displayKeyValue(path), err)
	}
	return pub, nil
}

// expandHome resolves a leading "~" against the user's home directory.
//
// A tilde that cannot be expanded is an ERROR, never a literal path segment.
// Leaving it in place makes every downstream step read a path that exists
// under no name, so "$HOME is not defined" surfaces as a missing key file and
// sends the user hunting for a key they already have. A path with no leading
// tilde is returned untouched.
func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand %q: %w", path, err)
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
	}
	return path, nil
}

func findByFingerprint(ag agent.Agent, fingerprint, source string) (*Discovered, error) {
	signers, err := ag.Signers()
	if err != nil {
		return nil, fmt.Errorf("listing ssh-agent identities: %w", err)
	}
	for _, s := range signers {
		if ssh.FingerprintSHA256(s.PublicKey()) == fingerprint {
			return &Discovered{Signer: s, Fingerprint: fingerprint, Source: source}, nil
		}
	}
	return nil, fmt.Errorf("no ssh-agent identity matches fingerprint %s — ssh-add the key first", fingerprint)
}

func findByPublicKey(ag agent.Agent, pub ssh.PublicKey, source string) (*Discovered, error) {
	signers, err := ag.Signers()
	if err != nil {
		return nil, fmt.Errorf("listing ssh-agent identities: %w", err)
	}
	for _, s := range signers {
		if bytes.Equal(s.PublicKey().Marshal(), pub.Marshal()) {
			return &Discovered{
				Signer:      s,
				Fingerprint: ssh.FingerprintSHA256(s.PublicKey()),
				Source:      source,
			}, nil
		}
	}
	return nil, fmt.Errorf("key %s is not loaded in ssh-agent", ssh.FingerprintSHA256(pub))
}

// IsHardwareBacked reports whether pub's key TYPE is self-identifying as
// hardware-backed (spec §9.1.2, posture P3): sk-ssh-ed25519@openssh.com or
// sk-ecdsa-sha2-nistp256@openssh.com. This is the ONLY signing posture ctxloom
// can detect honestly from the public key alone — whether a plain key is
// guarded by `ssh-add -c` (confirm-before-use) has no protocol-visible
// signal (agent.Agent.List returns key blob + comment, nothing else) and
// must be self-attested by the user, never inferred (spec §9.1.2: "I looked
// for another honest signal and there is none").
// An identity held as an OpenSSH CERTIFICATE reports the certificate's
// algorithm name, not the wrapped key's, so the posture must be read from the
// key the certificate is over: the token holds that key's private half either
// way. The reverse never holds — a certificate over a software key is still
// software.
func IsHardwareBacked(pub ssh.PublicKey) bool {
	if pub == nil {
		return false
	}
	if cert, ok := pub.(*ssh.Certificate); ok && cert.Key != nil {
		return IsHardwareBacked(cert.Key)
	}
	switch pub.Type() {
	case ssh.KeyAlgoSKED25519, ssh.KeyAlgoSKECDSA256,
		// A caller may hand us any ssh.PublicKey implementation, so the
		// certificate algorithm names are matched by name too rather than
		// relying on the concrete type alone.
		ssh.CertAlgoSKED25519v01, ssh.CertAlgoSKECDSA256v01:
		return true
	default:
		return false
	}
}

// candidatesFromSigners builds the ambiguous-choice candidate list. Comments
// come from List() (Signers() carries no comment), matched to each signer by
// public key blob; a lookup failure just omits the comment rather than
// failing the whole listing; this is display-only.
func candidatesFromSigners(ag agent.Agent, signers []ssh.Signer) []Candidate {
	comments := map[string]string{}
	if keys, err := ag.List(); err == nil {
		for _, k := range keys {
			comments[string(k.Blob)] = k.Comment
		}
	}
	out := make([]Candidate, 0, len(signers))
	for _, s := range signers {
		pub := s.PublicKey()
		out = append(out, Candidate{
			Fingerprint: ssh.FingerprintSHA256(pub),
			Type:        pub.Type(),
			Comment:     comments[string(pub.Marshal())],
		})
	}
	return out
}
