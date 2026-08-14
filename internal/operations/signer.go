// Signer management (signature-envelope spec §7.2): reading and writing the
// user's/project's allowed_signers trust root. CLI-only, never MCP (ADR
// 0024) — internal/cli/signer.go is the only production caller of the
// mutating functions here; nothing in the MCP surface reaches this file.
package operations

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// SignerNamespaceAliases maps the short vocabulary `ctxloom signer` accepts
// on --namespace (mirroring ssh-keygen/git vocabulary, ADR 0027) to the
// full spec namespace strings. "publish" is the default namespace when the
// CLI is given none (spec §7.2).
var SignerNamespaceAliases = map[string]string{
	"publish": signing.NamespacePublish,
	"approve": signing.NamespaceApprove,
	"reject":  signing.NamespaceReject,
}

// ResolveSignerNamespaces expands a list of short aliases ("publish",
// "approve", "reject") or already-full namespace strings into the full
// spec namespace strings `allowed_signers` stores. An empty input defaults
// to []string{NamespacePublish} — signer add's documented default.
func ResolveSignerNamespaces(aliases []string) ([]string, error) {
	if len(aliases) == 0 {
		return []string{signing.NamespacePublish}, nil
	}
	out := make([]string, 0, len(aliases))
	for _, a := range aliases {
		if full, ok := SignerNamespaceAliases[a]; ok {
			out = append(out, full)
			continue
		}
		// Already a full namespace string (e.g. from a scripted caller) —
		// accept it as-is rather than forcing every caller through the
		// alias table.
		if a == signing.NamespacePublish || a == signing.NamespaceApprove || a == signing.NamespaceReject {
			out = append(out, a)
			continue
		}
		return nil, fmt.Errorf("unknown namespace %q (want publish|approve|reject)", a)
	}
	return out, nil
}

// SignerKeyInfo is a public key resolved for signer management: enough to
// render a confirmation prompt (fingerprint) and, once confirmed, write an
// allowed_signers entry.
type SignerKeyInfo struct {
	PublicKey   ssh.PublicKey
	Fingerprint string
	Comment     string // from the key file/literal, if any — advisory only
}

// ResolveSignerKey reads and parses a public key for `signer add`. keyArg is
// a literal "ssh-<type> AAAA..." authorized-keys line, "-" for stdin, or a
// path to a file containing one. ctxloom only ever reads PUBLIC key
// material here — there is no path in this function that can return a
// private key (spec §9.1: key management never touches private material).
func ResolveSignerKey(keyArg string, fs afero.Fs, stdin io.Reader) (SignerKeyInfo, error) {
	if keyArg == "" {
		return SignerKeyInfo{}, fmt.Errorf("a public key is required (--key <path|->)")
	}

	var data []byte
	switch keyArg {
	case "-":
		b, err := io.ReadAll(stdin)
		if err != nil {
			return SignerKeyInfo{}, fmt.Errorf("reading public key from stdin: %w", err)
		}
		data = b
	default:
		if pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(keyArg)); err == nil {
			return keyInfoFromPublicKey(pub, comment), nil
		}
		b, err := afero.ReadFile(getFS(fs), keyArg)
		if err != nil {
			return SignerKeyInfo{}, fmt.Errorf("%q is not a recognized public key and could not be read as a file: %w", keyArg, err)
		}
		data = b
	}

	pub, comment, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return SignerKeyInfo{}, fmt.Errorf("no parseable SSH public key found: %w", err)
	}
	return keyInfoFromPublicKey(pub, comment), nil
}

func keyInfoFromPublicKey(pub ssh.PublicKey, comment string) SignerKeyInfo {
	return SignerKeyInfo{
		PublicKey:   pub,
		Fingerprint: ssh.FingerprintSHA256(pub),
		Comment:     comment,
	}
}

// signerStorePath resolves which allowed_signers file `signer remove` writes
// to: the committable project store (.ctxloom/allowed_signers) when project
// is true, else the user store (~/.ctxloom/allowed_signers) — spec §7,
// locations 2 and 3. Locations are chosen explicitly, never inferred,
// because writing to the wrong one is a trust-root mistake. A project
// request with none configured is a hard error here — unlike
// resolveSignerAddPath below, `signer untrust --project` outside a project
// has nothing sensible to fall back to (removing from a user store the
// caller never asked about would be its own surprise).
func signerStorePath(cfg *config.Config, project bool) (string, error) {
	if project {
		if cfg == nil || len(cfg.GetAppPaths()) == 0 {
			return "", fmt.Errorf("no .ctxloom directory configured — cannot resolve the project allowed_signers path")
		}
		return paths.AllowedSignersPath(cfg.GetAppPaths()[0]), nil
	}
	return paths.HomeAllowedSignersPath()
}

// resolveSignerAddPath resolves the allowed_signers file `signer trust`
// (AddSigner) writes to. project is now the DEFAULT posture (the committable
// project store is what makes team trust work — a colleague who clones the
// repo inherits it, rather than every colleague trusting the publisher
// individually), so unlike signerStorePath a project request with none
// configured does not fail: it falls back to the user store, and says so via
// the returned usedProject/fallbackReason — silently writing somewhere other
// than the project store a caller asked for is exactly the defect shape this
// project keeps removing.
func resolveSignerAddPath(cfg *config.Config, project bool) (path string, usedProject bool, fallbackReason string, err error) {
	if project && cfg != nil && len(cfg.GetAppPaths()) > 0 {
		// A non-empty app path is NOT evidence of a project: outside one it
		// resolves to the HOME app dir, and taking this branch there writes
		// the user store while reporting the project store — the exact
		// silent-wrong-destination this function exists to prevent. Compare
		// against home so only a genuine project checkout qualifies.
		candidate := paths.AllowedSignersPath(cfg.GetAppPaths()[0])
		if home, herr := paths.HomeAllowedSignersPath(); herr != nil || candidate != home {
			return candidate, true, "", nil
		}
	}
	path, err = paths.HomeAllowedSignersPath()
	if err != nil {
		return "", false, "", err
	}
	if project {
		fallbackReason = "no project (.ctxloom directory) found in this checkout — falling back to your user store"
	}
	return path, false, fallbackReason, nil
}

// AddSignerRequest is the input to AddSigner.
type AddSignerRequest struct {
	Principal  string
	Key        SignerKeyInfo
	Namespaces []string // full namespace strings; see ResolveSignerNamespaces
	// Comment overrides Key.Comment when non-empty.
	Comment string
	// Project writes to the committable project store instead of the user
	// store.
	Project bool
	FS      afero.Fs
}

// AddSignerResult reports what was written and where.
type AddSignerResult struct {
	Path        string
	Line        string
	Fingerprint string
	// Fallback is true when req.Project asked for the committable project
	// store but none was configured, so this call wrote to the user store
	// instead of failing. The CLI must SAY SO — see FallbackReason.
	Fallback bool
	// FallbackReason explains why, for CLI output. Empty unless Fallback.
	FallbackReason string
}

// AddSigner appends one allowed_signers entry, creating the file (and its
// parent directory) if needed. It performs no confirmation prompt — that is
// the CLI's job (signer add is the single most dangerous command in this
// feature and the confirmation must show the fingerprint BEFORE this
// function is ever called, spec §7.2).
func AddSigner(cfg *config.Config, req AddSignerRequest) (*AddSignerResult, error) {
	if req.Principal == "" {
		return nil, fmt.Errorf("a principal is required")
	}
	if req.Key.PublicKey == nil {
		return nil, fmt.Errorf("a resolved public key is required")
	}
	namespaces := req.Namespaces
	if namespaces == nil {
		var err error
		namespaces, err = ResolveSignerNamespaces(nil)
		if err != nil {
			return nil, err
		}
	}

	path, usedProject, fallbackReason, err := resolveSignerAddPath(cfg, req.Project)
	if err != nil {
		return nil, err
	}

	comment := req.Comment
	if comment == "" {
		comment = req.Key.Comment
	}
	line, err := allowedsigners.FormatEntry(allowedsigners.Entry{
		Principals: []string{req.Principal},
		Namespaces: namespaces,
		KeyType:    req.Key.PublicKey.Type(),
		PublicKey:  req.Key.PublicKey,
		Comment:    comment,
	})
	if err != nil {
		return nil, err
	}

	fs := getFS(req.FS)
	if err := appendAllowedSignersLine(fs, path, line); err != nil {
		return nil, err
	}

	return &AddSignerResult{
		Path:           path,
		Line:           line,
		Fingerprint:    req.Key.Fingerprint,
		Fallback:       req.Project && !usedProject,
		FallbackReason: fallbackReason,
	}, nil
}

// appendAllowedSignersLine creates dirs/file as needed and appends line,
// preserving whatever already exists.
func appendAllowedSignersLine(fs afero.Fs, path, line string) error {
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	existing, _ := afero.ReadFile(fs, path) // absent is fine: existing stays nil
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString(line)
	b.WriteString("\n")

	// This is the trust root — a crash or concurrent read mid-write must
	// never observe a truncated/partial allowed_signers file. Atomic write
	// via a same-dir temp file + rename (dir already created above). Durable:
	// a human's trust decision is unrecoverable if the rename silently
	// reverts after a crash — there is no other record of it.
	if err := iox.WriteFileAtomicFs(fs, path, []byte(b.String()), 0o600, iox.Durable()); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// SignerListing is one allowed_signers entry plus the store it came from,
// for `signer list`/`signer show` display.
type SignerListing struct {
	Entry allowedsigners.Entry
	// Source is "embedded" | "user" | "project".
	Source string
	Path   string
	// Suppressed marks an "embedded" entry this machine has locally
	// DISTRUSTED (see config.SuppressedEmbeddedPrincipals /
	// RemoveSigner's embedded-suppression path below). Always false for
	// "user"/"project" entries — removing one of those deletes the line
	// outright, there is nothing left to tag. A suppressed embedded entry is
	// still LISTED (visibility never regresses just because it was acted on)
	// but config.TrustRoot() excludes it from the union, so it grants no
	// trust even though `signer list`/`show` still shows it — the exact
	// parallel to a removed user/project entry, which also stays visible in
	// nothing but git history, never hidden.
	Suppressed bool
	// Unreadable, when non-empty, means this row is NOT an entry: it is a
	// line in Path the parser could not turn into one, carried into the
	// listing so `signer list` cannot present a silently-shortened store as
	// the whole truth. Entry is zero for such a row. An audit surface that
	// omits what it could not read is worse than one that shows a gap.
	Unreadable string
}

// ListSigners returns every entry across all three trust-root locations
// (embedded defaults, user store, project store — spec §7), each tagged
// with the store it came from. Malformed lines are silently omitted (they
// grant no trust and Parse already reports them via clidiag warnings at
// config load time — cfg.TrustRoot() is the load-bearing union; this
// function is display-only and re-parses the same files to retain
// per-entry Source tagging that Union collapses).
//
// The embedded trust root (ctxloom's own compiled-in release/bundle key(s) —
// spec §7, location 1) IS surfaced here as individual entries, tagged
// "embedded": an operator auditing "whom do I trust to publish?" must be able
// to see every key that grants trust, including the one compiled into the
// binary — hiding it here was itself a defect (this function used to omit
// it entirely, justified by a comment claiming the embedded root was "empty
// today"; that stopped being true the moment a release key was actually
// embedded). config.EmbeddedSigners() returns a READ view — the
// Store type exposes no mutator — so
// this enumerates entries with no mutation path opening up. An embedded entry
// is NOT removable via this CLI (only a new binary changes the compiled-in
// bytes) — RemoveSigner reports that honestly, and can instead persist a
// local suppression (Suppressed above) that TrustRoot() subtracts.
func ListSigners(cfg *config.Config, fs afero.Fs) ([]SignerListing, error) {
	fs = getFS(fs)
	var out []SignerListing

	suppressed := map[string]bool{}
	if cfg != nil {
		suppressed = cfg.SuppressedEmbeddedPrincipals()
	}
	for _, e := range config.EmbeddedSigners().Entries() {
		out = append(out, SignerListing{
			Entry:      e,
			Source:     "embedded",
			Path:       "(compiled-in)",
			Suppressed: e.MatchesAnyPrincipal(suppressed),
		})
	}

	if home, err := paths.HomeAllowedSignersPath(); err == nil {
		out = append(out, listFromPath(fs, home, "user")...)
	}
	if cfg != nil && len(cfg.GetAppPaths()) > 0 {
		project := paths.AllowedSignersPath(cfg.GetAppPaths()[0])
		out = append(out, listFromPath(fs, project, "project")...)
	}
	return out, nil
}

// listFromPath reads one on-disk store for the audit listing.
//
// It stays TOLERANT — a half-authored store must not blank the listing — but
// never SILENT: an absent file is genuinely nothing to list, while a file
// that cannot be opened, cannot be read, or holds lines the parser dropped is
// a listing that is shorter than the truth, and an audit surface that hides
// that is worse than one that shows a gap.
func listFromPath(fs afero.Fs, path, source string) []SignerListing {
	f, err := fs.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no store here yet — legitimately nothing to list
		}
		clidiag.Warn("ctxloom", "signer list: cannot open %s, its entries are missing from this listing: %v", path, err)
		return []SignerListing{{Source: source, Path: path, Unreadable: err.Error()}}
	}
	defer func() { _ = f.Close() }()

	store, _, err := allowedsigners.Parse(f)
	if err != nil {
		clidiag.Warn("ctxloom", "signer list: cannot read %s, its entries are missing from this listing: %v", path, err)
		return []SignerListing{{Source: source, Path: path, Unreadable: err.Error()}}
	}
	var out []SignerListing
	for _, e := range store.Entries() {
		out = append(out, SignerListing{Entry: e, Source: source, Path: path})
	}
	for _, pe := range store.ParseErrors() {
		clidiag.Warn("ctxloom", "signer list: %s line %d is not a usable entry and grants no trust: %v", path, pe.Line, pe.Err)
		out = append(out, SignerListing{Source: source, Path: path, Unreadable: pe.Error()})
	}
	return out
}

// ShowSigner returns every entry (across all stores) whose principals match
// identity — the `signer show <principal>` surface.
func ShowSigner(cfg *config.Config, identity string, fs afero.Fs) ([]SignerListing, error) {
	all, err := ListSigners(cfg, fs)
	if err != nil {
		return nil, err
	}
	var out []SignerListing
	for _, l := range all {
		// An unreadable line cannot be matched against identity, and dropping
		// it here would make `signer show X` answer "nothing" for a store
		// that may well hold an entry for X — the same lie `signer list` used
		// to tell, on the surface an operator reaches for to check ONE key.
		if l.Unreadable != "" || l.Entry.MatchesPrincipal(identity) {
			out = append(out, l)
		}
	}
	return out, nil
}

// RemoveSignerRequest is the input to RemoveSigner.
type RemoveSignerRequest struct {
	Principal string
	Project   bool
	FS        afero.Fs
}

// RemoveSignerResult reports how many entries were removed and from where.
type RemoveSignerResult struct {
	Path    string
	Removed int
	// EmbeddedSuppressed is true when Principal ALSO matched ctxloom's
	// EMBEDDED trust root (spec §7, location 1), in which case this call
	// additionally persisted a local suppression record: ADDITIVE with
	// Removed, which counts on-disk allowed_signers lines actually deleted
	// — a principal can be both an on-disk entry and an embedded one, and
	// both effects must land (an early return on Removed>0 used to skip the
	// embedded check entirely, leaving the embedded key trusted after the
	// on-disk line was deleted). The embedded key's compiled-in bytes are
	// never touched —
	// only a new binary changes those — but the suppression is REAL:
	// config.TrustRoot() subtracts the matching embedded entry from the
	// trust root on every subsequent decision, so content signed only by
	// that key is withheld from here on (this machine, or this project
	// with --project).
	EmbeddedSuppressed bool
	// SuppressionPath is the distrusted_signers file EmbeddedSuppressed was
	// recorded in, when applicable ("" otherwise).
	SuppressionPath string
}

// RemoveSigner deletes every line in the target store (user or project)
// whose principals list contains exactly req.Principal (a literal match,
// not a glob — removing "*@example.com" removes only that literal pattern
// entry, never every entry it happens to match, so this can never silently
// drop more trust than asked). Entries in the OTHER of user/project are
// untouched: removing a signer only ever narrows the store it was written to.
//
// The embedded trust root is a separate, ADDITIVE case: this command can
// never delete its compiled-in bytes, but whenever Principal ALSO names an
// embedded entry, RemoveSigner ALSO persists a LOCAL distrust record (see
// EmbeddedSuppressed/suppressEmbeddedPrincipal) — regardless of whether an
// on-disk line was removed. A principal can be BOTH an on-disk
// allowed_signers line and an embedded entry at once; RemoveSigner used to
// return as soon as the on-disk removal counted anything, silently
// skipping the embedded check and leaving the embedded key trusted after the
// on-disk line was gone. This is the practical equivalent of removal for a
// root nothing can literally edit, and it is a REAL effect, not a message:
// TrustRoot() (spec §7, §9.2) honors it on every subsequent decision.
func RemoveSigner(cfg *config.Config, req RemoveSignerRequest) (*RemoveSignerResult, error) {
	if req.Principal == "" {
		return nil, fmt.Errorf("a principal is required")
	}
	path, err := signerStorePath(cfg, req.Project)
	if err != nil {
		return nil, err
	}
	fs := getFS(req.FS)

	removed, err := removeFromAllowedSignersFile(fs, path, req.Principal)
	if err != nil {
		return nil, err
	}
	result := &RemoveSignerResult{Path: path, Removed: removed}

	if entry := matchingEmbeddedEntry(req.Principal); entry != nil {
		suppressionPath, serr := suppressEmbeddedPrincipal(fs, cfg, *entry, req.Project)
		if serr != nil {
			return nil, serr
		}
		result.EmbeddedSuppressed = true
		result.SuppressionPath = suppressionPath
	}

	return result, nil
}

// removeFromAllowedSignersFile deletes every line in path whose principals
// list contains exactly principal (literal match, see RemoveSigner's doc),
// returning how many entries were removed. An absent file removes nothing
// (not an error) — the ordinary case when no on-disk store has this
// principal at all, including every embedded-only principal.
func removeFromAllowedSignersFile(fs afero.Fs, path, principal string) (int, error) {
	f, err := fs.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // no store here — legitimately nothing to remove
		}
		// "I could not open the store" is not "the principal is not in it".
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	lines, err := readLines(f)
	_ = f.Close()
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	store, _, err := allowedsigners.Parse(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}

	toDrop := map[int]bool{}
	removed := 0
	for _, e := range store.Entries() {
		if slices.Contains(e.Principals, principal) {
			toDrop[e.Line] = true
			removed++
		}
	}
	if removed == 0 {
		// A dropped line contributes no entry, so the principal LOOKS absent
		// — and `signer remove` would report "no entry for X", telling an
		// operator a key is untrusted while a line they cannot see stays in
		// the file. Nothing removed because the store could not be read in
		// full is not the same as nothing to remove. (When something WAS
		// removed the command did its job; the unrelated bad line is only
		// worth a warning, emitted below.)
		if perrs := store.ParseErrors(); len(perrs) > 0 {
			return 0, fmt.Errorf("removed nothing from %s, but %d line(s) there could not be read and may hold this principal — fix them and retry: %w",
				path, len(perrs), perrs[0])
		}
		return 0, nil
	}
	for _, pe := range store.ParseErrors() {
		clidiag.Warn("ctxloom", "signer remove: %s line %d could not be read and was left in place: %v", path, pe.Line, pe.Err)
	}

	var kept []string
	for i, line := range lines {
		if toDrop[i+1] { // Entry.Line is 1-based
			continue
		}
		kept = append(kept, line)
	}

	out := strings.Join(kept, "\n")
	if len(kept) > 0 {
		out += "\n"
	}
	// Same atomic-write requirement as appendAllowedSignersLine — the trust
	// root must never be observed half-rewritten. Removing the LAST entry
	// legitimately empties this file (kept == nil, out == ""): AllowEmpty is
	// required here, not a bug being masked — the alternative is a "remove"
	// that refuses to ever remove the final principal. Durable: same
	// unrecoverable-human-decision reasoning as the append side — a
	// distrust that silently reverts after a crash is worse than one that
	// never happened, because nothing tells the human it needs re-doing.
	if err := iox.WriteFileAtomicFs(fs, path, []byte(out), 0o600, iox.AllowEmpty(), iox.Durable()); err != nil {
		return 0, fmt.Errorf("write %s: %w", path, err)
	}
	return removed, nil
}

// matchingEmbeddedEntry returns the first entry in ctxloom's compiled-in
// trust root (config.EmbeddedSigners()) whose Principals pattern-list
// matches principal (Entry.MatchesPrincipal — ssh_config PATTERNS, may be a
// glob), or nil if none does. Callers need the matched ENTRY, not just a
// bool: suppressEmbeddedPrincipal must record what the entry's own
// Principals actually say, not the (possibly glob-expanded) identity the
// user typed — see its doc for why.
func matchingEmbeddedEntry(principal string) *allowedsigners.Entry {
	for _, e := range config.EmbeddedSigners().Entries() {
		if e.MatchesPrincipal(principal) {
			entry := e
			return &entry
		}
	}
	return nil
}

// distrustedSignersStorePath resolves which distrusted_signers file A2's
// embedded-suppression write targets — the SAME user/project choice
// signerStorePath makes for allowed_signers, so a team distributes "we no
// longer trust the embedded key" via --project exactly like they distribute
// a trust decision.
func distrustedSignersStorePath(cfg *config.Config, project bool) (string, error) {
	if project {
		if cfg == nil || len(cfg.GetAppPaths()) == 0 {
			return "", fmt.Errorf("no .ctxloom directory configured — cannot resolve the project distrusted_signers path")
		}
		return paths.DistrustedSignersPath(cfg.GetAppPaths()[0]), nil
	}
	return paths.HomeDistrustedSignersPath()
}

// suppressEmbeddedPrincipal persists a LOCAL record that entry — an embedded
// entry matchingEmbeddedEntry matched by GLOB (Entry.MatchesPrincipal,
// ssh_config PATTERNS) against the principal the user typed — is no longer
// trusted. The subtraction config.TrustRoot() (via filterSuppressedPrincipals
// -> Entry.MatchesAnyPrincipal) honors on every future decision is a LITERAL
// membership check against the embedded entry's OWN Principals strings, not
// a glob match. Recording the user-typed identity instead of entry's actual
// Principals would report success here while never actually suppressing
// anything for a glob-principal embedded entry: the read side's literal
// check would never find the typed identity among the entry's own (glob)
// Principals. Recording every one of entry's own Principals strings
// closes that gap and is what the read side's literal check needs to see.
// Idempotent: a principal already recorded is left as-is, never duplicated.
func suppressEmbeddedPrincipal(fs afero.Fs, cfg *config.Config, entry allowedsigners.Entry, project bool) (string, error) {
	path, err := distrustedSignersStorePath(cfg, project)
	if err != nil {
		return "", err
	}
	existing, _ := afero.ReadFile(fs, path) // absent is fine: existing stays nil
	already := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		already[strings.TrimSpace(line)] = true
	}
	for _, principal := range entry.Principals {
		if already[principal] {
			continue // already suppressed
		}
		if err := appendAllowedSignersLine(fs, path, principal); err != nil {
			return "", err
		}
		already[principal] = true
	}
	return path, nil
}

func readLines(r io.Reader) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}
