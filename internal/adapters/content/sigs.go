package content

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// SigDirName is the bundle-root directory holding stored signatures.
//
// It is exported because a caller that SIGNS a tree has to be able to say where
// the signature landed (operations.SignBundleResult.SigPath).
//
// Signatures are keyed by CONTENT HASH, not attached to a path. That follows
// through on the property the preimage already has — it binds content bytes,
// not name or location — instead of contradicting it in storage. Concretely:
// renaming or moving a file cannot orphan its signature, byte-identical content
// in two places shares one signature, and, because the raw and distilled forms
// of an item are separate files with separate digests, a signature over the raw
// form can never be found for the distilled one. That last property falls out of
// the storage model here rather than being enforced by a check somewhere.
//
// The directory is dot-prefixed and therefore skipped by TreeStore.Bundles, so it
// can never be mistaken for a bundle.
const SigDirName = ".sigs"

// namespacePattern is the conservative charset a namespace must match to become
// part of a filename. The three real namespaces ("publish.v1.ctxloom.dev" and
// its siblings) satisfy it; anything with a separator or a wildcard in it is
// refused rather than sanitised, because a namespace is matched byte-for-byte on
// the verifying side and a silently-rewritten one would key a signature under an
// assertion nobody made.
var namespacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validateNamespace(ns Namespace) error {
	if !namespacePattern.MatchString(string(ns)) {
		return fmt.Errorf("%w: namespace %q must match %s", ErrBadPath, ns, namespacePattern)
	}
	return nil
}

// contentKey is the lookup key for a form's signatures: the hex sha256 of its
// content digest.
func contentKey(digest []byte) string {
	sum := sha256.Sum256(digest)
	return hex.EncodeToString(sum[:])
}

// sigFileName builds "<content-key>.<namespace>.<signer-tag>.sig".
//
// The trailing tag names the SIGNING KEY, so the store holds one entry per
// (key, namespace) over a given content: a re-sign by the same key REPLACES
// its earlier entry — a stale signature over a manifest the tree no longer
// has does not linger beside the live one for a reader to arbitrate — while
// two different keys over the same content (the mixed-provenance case, two
// maintainers signing one item) keep two entries.
//
// The tag is derived from a key the CALLER hands in, never from the signature
// blob: layer 0 does not parse a signature, because parsing it is the first
// step of interpreting it, and interpretation belongs to layer 2. The tag is
// the entry's filing identity and nothing more — no reader resolves who signed
// from a filename; that is VerifyPublisher's job over the bytes and the trust
// root. A caller that filed a signature under the wrong key has misfiled it,
// not forged trust.
func sigFileName(contentKey string, ns Namespace, by ssh.PublicKey) string {
	return contentKey + "." + string(ns) + "." + signerTag(by) + ".sig"
}

// signerTag is a key's filing name: the hex SHA-256 of its wire form — the
// same digest ssh's SHA256 fingerprint spells in base64, in the charset a
// filename and parseSigFileName's last-dot split both accept.
func signerTag(by ssh.PublicKey) string {
	sum := sha256.Sum256(by.Marshal())
	return hex.EncodeToString(sum[:])
}

// parseSigFileName recovers the namespace from a signature filename. The
// signer tag never contains a dot, so the namespace is everything between the
// content key and the final dot-separated field — which is why namespaces
// containing dots (they all do) round-trip correctly. The tag itself is
// opaque here: an entry filed before entries were keyed by signing key (its
// tag was derived from the signature's bytes) parses and is read exactly as a
// current one is.
func parseSigFileName(contentKey, name string) (Namespace, bool) {
	rest, ok := strings.CutPrefix(name, contentKey+".")
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, ".sig")
	if !ok {
		return "", false
	}
	idx := strings.LastIndex(rest, ".")
	if idx <= 0 {
		return "", false
	}
	ns := Namespace(rest[:idx])
	if validateNamespace(ns) != nil {
		return "", false
	}
	return ns, true
}

// readSignatures loads every signature stored against a content key. bundleDir
// is store-relative and slash-separated: signatures are READ through the TreeFS
// seam, so a pinned remote serves them exactly as an authored tree does.
func readSignatures(tfs TreeFS, bundleDir, key string) (SigSet, error) {
	dir := path.Join(bundleDir, SigDirName)
	entries, err := tfs.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No signature directory is NORMAL, not an error: whole-bundle
			// signing is the common case and most items carry no signature of
			// their own. Reporting an error here would make "unsigned" and
			// "unreadable" indistinguishable to the layer above, and only one
			// of those is safe to treat as "no records".
			return nil, nil
		}
		return nil, fmt.Errorf("content: reading signature store %q: %w", dir, err)
	}
	var out SigSet
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		ns, ok := parseSigFileName(key, e.Name)
		if !ok {
			continue
		}
		data, err := tfs.ReadFile(path.Join(dir, e.Name))
		if err != nil {
			return nil, fmt.Errorf("content: reading signature %q: %w", e.Name, err)
		}
		out = append(out, Signature{Namespace: ns, Bytes: data})
	}
	sortSigs(out)
	return out, nil
}

// writeSignature stores signature bytes against a content key, filed under
// the signing key: a second write by the same key in the same namespace
// replaces the first.
func writeSignature(fsys afero.Fs, bundleDir, key string, ns Namespace, by ssh.PublicKey, sig []byte) error {
	if err := validateNamespace(ns); err != nil {
		return err
	}
	if by == nil {
		return errors.New("content: refusing to store a signature with no signing key to file it under")
	}
	if len(sig) == 0 {
		return errors.New("content: refusing to store an empty signature")
	}
	dir := filepath.Join(bundleDir, SigDirName)
	if err := fsys.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("content: creating signature store %q: %w", dir, err)
	}
	target := filepath.Join(dir, sigFileName(key, ns, by))
	// No AllowEmpty: an empty sig is already refused above. A re-write at the
	// same path is the replace this store promises, made atomic so a reader
	// never sees a torn entry.
	if err := iox.WriteFileAtomicFs(fsys, target, sig, 0o644); err != nil {
		return fmt.Errorf("content: writing signature %q: %w", target, err)
	}
	return nil
}
