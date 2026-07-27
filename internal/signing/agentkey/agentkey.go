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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

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

func (e *AmbiguousKeyError) Error() string {
	var b strings.Builder
	b.WriteString("ctxloom: multiple keys in ssh-agent — which should sign?\n\n")
	for _, c := range e.Candidates {
		fmt.Fprintf(&b, "  %s  %s", c.Fingerprint, c.Type)
		if c.Comment != "" {
			fmt.Fprintf(&b, "  (%s)", c.Comment)
		}
		b.WriteString("\n")
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

func (e *AmbiguousKeyNameError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ctxloom: --key %q matches %d keys in ssh-agent.\n\n", e.Name, len(e.Candidates))
	for _, c := range e.Candidates {
		fmt.Fprintf(&b, "  %s  %s", c.Fingerprint, c.Type)
		if c.Comment != "" {
			fmt.Fprintf(&b, "  (%s)", c.Comment)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nNarrow the name, or disambiguate with the fingerprint:\n")
	if len(e.Candidates) > 0 {
		fmt.Fprintf(&b, "  ctxloom sign <bundle> --key %s\n", e.Candidates[0].Fingerprint)
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
	// Err is the underlying error Detail was derived from, when one exists
	// (U135-F01/F03): an ssh-agent RPC/protocol failure (agent locked, a
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
	b.WriteString("To publish unsigned anyway: ctxloom fragment push my-frag --no-sign\n")
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

// NewDiscoverer returns a Discoverer wired to the real git binary and the
// real ssh-agent named by SSH_AUTH_SOCK — the production configuration.
func NewDiscoverer() *Discoverer {
	return &Discoverer{
		GitConfig: execGitConfig,
		DialAgent: dialEnvAgent,
		ReadFile:  os.ReadFile,
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
		return "", false, fmt.Errorf("%s: %w", strings.TrimSpace(stderr.String()), err)
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
	return agent.NewClient(conn), nil
}

// Discover resolves a signing identity. explicitKey is the caller's
// already-merged --key/sign.key value (empty when neither was given); the
// caller is responsible for that precedence merge (an explicit flag beating
// a config default), Discover only knows "was one supplied or not".
func (d *Discoverer) Discover(ctx context.Context, explicitKey string) (*Discovered, error) {
	if explicitKey != "" {
		return d.resolveExplicit(ctx, explicitKey)
	}

	gitKey, ok, err := d.GitConfig(ctx, d.Dir, "user.signingkey")
	if err != nil {
		return nil, fmt.Errorf("reading git config user.signingkey: %w", err)
	}
	if ok && gitKey != "" {
		return d.resolveGitSigningKey(ctx, gitKey)
	}

	return d.resolveSoleAgentIdentity(ctx, []string{"git config user.signingkey", "ssh-agent identities"})
}

// resolveExplicit resolves --key/sign.key in fallback order: (a) a SHA256
// fingerprint, (b) a key line/literal/path to a public key, (c) — only when
// (b) fails to produce a key at all — a case-insensitive substring match
// against a live ssh-agent identity's COMMENT (the name ctxloom itself
// prints in the ambiguous-key listing, e.g. "ben@abbitt.me"). (a) and (b)
// must keep priority over (c): a value that legitimately parses as a
// fingerprint or resolves as a file must never be reinterpreted as a name
// just because it happens to also resemble one.
func (d *Discoverer) resolveExplicit(ctx context.Context, explicitKey string) (*Discovered, error) {
	ag, err := d.DialAgent()
	if err != nil {
		return nil, &NoKeyError{
			Looked: []string{"--key/sign.key " + explicitKey},
			Detail: err.Error(),
			Err:    err,
		}
	}

	if strings.HasPrefix(explicitKey, "SHA256:") {
		return findByFingerprint(ag, explicitKey, "--key")
	}

	pub, pubErr := d.resolvePublicKey(explicitKey)
	if pubErr == nil {
		return findByPublicKey(ag, pub, "--key")
	}

	discovered, nameErr := d.resolveByComment(ag, explicitKey)
	if nameErr == nil {
		return discovered, nil
	}
	var ambigName *AmbiguousKeyNameError
	if errors.As(nameErr, &ambigName) {
		// Ambiguity is a hard error — never guess, and never mask it behind
		// the earlier "not a recognized public key" message.
		return nil, nameErr
	}

	// U135-F01(b): nameErr is not just "no comment matched" — resolveByComment
	// also returns "listing ssh-agent identities: <rpc err>" when ag.Signers()
	// itself fails (agent locked, wedged socket). That used to be silently
	// dropped here in favor of pubErr alone, so an agent RPC failure was
	// reported as "not a recognized fingerprint, public key, or ssh-agent key
	// name" — true but misleading about WHY. Chain nameErr too.
	return nil, fmt.Errorf("--key %q: not a recognized fingerprint or public key (%v); and %w", explicitKey, pubErr, nameErr)
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
		return nil, fmt.Errorf("no ssh-agent identity comment matches %q", explicitKey)
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
func (d *Discoverer) resolveGitSigningKey(ctx context.Context, value string) (*Discovered, error) {
	pub, err := d.resolvePublicKey(value)
	if err != nil {
		return nil, fmt.Errorf("git config user.signingkey %q: %w", value, err)
	}

	ag, err := d.DialAgent()
	if err != nil {
		return nil, &NoKeyError{
			Looked: []string{"git config user.signingkey"},
			Detail: fmt.Sprintf("git names %s, but %s", ssh.FingerprintSHA256(pub), err),
			Err:    err,
		}
	}

	d2, err := findByPublicKey(ag, pub, "git config user.signingkey")
	if err != nil {
		// U135-F01(a): findByPublicKey fails BOTH when the key is genuinely
		// absent from the agent AND when ag.Signers() itself errored (agent
		// locked, wedged socket, RPC failure) — those are different facts,
		// and the second one is not fixed by "ssh-add it": the key IS loaded,
		// the agent just could not be asked. Surface findByPublicKey's own
		// message (and chain its cause via Err) instead of guessing.
		return nil, &NoKeyError{
			Looked: []string{"git config user.signingkey"},
			Detail: fmt.Sprintf("git names %s, but %v", ssh.FingerprintSHA256(pub), err),
			Err:    err,
		}
	}
	return d2, nil
}

// resolveSoleAgentIdentity implements step 3 of the chain: use the agent's
// only identity when there is exactly one, error (ambiguous or empty)
// otherwise.
func (d *Discoverer) resolveSoleAgentIdentity(ctx context.Context, looked []string) (*Discovered, error) {
	ag, err := d.DialAgent()
	if err != nil {
		return nil, &NoKeyError{Looked: looked, Detail: err.Error(), Err: err}
	}

	signers, err := ag.Signers()
	if err != nil {
		// U135-F01(c)/F03: an ssh-agent RPC failure while LISTING identities
		// is a different fact than "the agent holds no identities" — chain
		// the real cause via Err so a caller can errors.Is/As it, not just
		// read a flattened string.
		return nil, &NoKeyError{Looked: looked, Detail: fmt.Sprintf("listing ssh-agent identities: %v", err), Err: err}
	}
	if len(signers) == 0 {
		return nil, &NoKeyError{Looked: looked}
	}
	if len(signers) > 1 {
		return nil, &AmbiguousKeyError{Candidates: candidatesFromSigners(ag, signers)}
	}

	s := signers[0]
	return &Discovered{
		Signer:      s,
		Fingerprint: ssh.FingerprintSHA256(s.PublicKey()),
		Source:      "ssh-agent (sole identity)",
	}, nil
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

	path := expandHome(value)
	data, err := d.ReadFile(path)
	if err != nil {
		// Try the .pub sibling — user.signingkey conventionally names the
		// PRIVATE key path in some setups; the public key lives alongside it.
		if !strings.HasSuffix(path, ".pub") {
			if data2, err2 := d.ReadFile(path + ".pub"); err2 == nil {
				data = data2
				err = nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("not a recognized public key and unreadable as a file: %w", err)
		}
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return nil, fmt.Errorf("%s does not contain a parseable SSH public key: %w", path, err)
	}
	return pub, nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
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
func IsHardwareBacked(pub ssh.PublicKey) bool {
	if pub == nil {
		return false
	}
	switch pub.Type() {
	case ssh.KeyAlgoSKED25519, ssh.KeyAlgoSKECDSA256:
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
