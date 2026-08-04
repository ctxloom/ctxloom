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
)

// SigDirName is the bundle-root directory holding stored signatures.
//
// It is exported because a caller that SIGNS a tree has to be able to say where
// the signature landed (operations.SignBundleResult.SigPath): a tree signature's
// filename derives from the signature's own bytes, so the store directory is the
// only stable path there is to name.
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

// sigFileName builds "<content-key>.<namespace>.<sig-tag>.sig".
//
// The trailing tag is derived from the signature's own bytes, so storing the
// same signature twice is idempotent while two DIFFERENT signatures over the
// same content under the same namespace — the mixed-provenance case, two
// maintainers signing one item — cannot collide. Deriving it from the signature
// rather than from the signer's key is deliberate: layer 0 must not parse a
// signature blob, because parsing it is the first step of interpreting it, and
// interpretation belongs to layer 2.
func sigFileName(contentKey string, ns Namespace, sig []byte) string {
	sum := sha256.Sum256(sig)
	return contentKey + "." + string(ns) + "." + hex.EncodeToString(sum[:])[:16] + ".sig"
}

// parseSigFileName recovers the namespace from a signature filename. The
// signature tag never contains a dot, so the namespace is everything between the
// content key and the final dot-separated field — which is why namespaces
// containing dots (they all do) round-trip correctly.
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

// writeSignature stores signature bytes against a content key.
func writeSignature(fsys afero.Fs, bundleDir, key string, ns Namespace, sig []byte) error {
	if err := validateNamespace(ns); err != nil {
		return err
	}
	if len(sig) == 0 {
		return errors.New("content: refusing to store an empty signature")
	}
	dir := filepath.Join(bundleDir, SigDirName)
	if err := fsys.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("content: creating signature store %q: %w", dir, err)
	}
	target := filepath.Join(dir, sigFileName(key, ns, sig))
	if err := afero.WriteFile(fsys, target, sig, 0o644); err != nil {
		return fmt.Errorf("content: writing signature %q: %w", target, err)
	}
	return nil
}
