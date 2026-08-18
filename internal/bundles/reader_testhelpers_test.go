package bundles

import "context"

// staticReader is the in-package seam a test uses to hand the loader content it
// did not have to write to a filesystem.
//
// It lives in a _test.go file ON PURPOSE. Its constructors mint provenance and
// a trust context, which is exactly what no exported constructor may do — a
// caller anywhere in the tree that could ask for "local, please" would be a
// trust bypass with a struct literal for a weapon. Keeping it here means it is
// compiled only into this package's tests and is unreachable from anywhere
// else.
type staticReader struct{ reads []BundleRead }

func (r staticReader) Read(context.Context) ([]BundleRead, error) { return r.reads, nil }

// seedLocal presents already-parsed bundles as local project content, keyed by
// resolution ref — the shape the retired WithSeededBundles option produced.
//
// Local, not remote, deliberately: a test that wants REMOTE content should go
// through NewRepoFSReader over real bytes, because the remote rows are the ones
// where the signature facts decide anything.
func seedLocal(seeded map[string]*Bundle) Reader {
	const (
		prov = ProvenanceProject
		tctx = TrustCtxLocal
	)
	var reads []BundleRead
	for ref, b := range seeded {
		if b == nil {
			continue
		}
		// The seed key is the bundle's resolution identity; a bundle that does
		// not carry its own name would compose broken item names ("/<item>"),
		// so backfill from the key, and record it as the source ref so the
		// content gate keys by it (honest local-vs-clone locality) rather than
		// the short bundle name.
		if b.Name == "" {
			b.Name = ref
		}
		b.sourceRef = ref
		reads = append(reads, newRead(ref, b, prov, tctx,
			signatureFacts{signature: SignatureNone, signer: SignerNone}))
	}
	return staticReader{reads: reads}
}
