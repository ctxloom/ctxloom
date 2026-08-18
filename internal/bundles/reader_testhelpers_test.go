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
		// so backfill from the key. sourceRef/sourceRefTyped are left UNSET
		// here so newRead's only-if-empty fallback stamps both together
		// (string AND typed) from the same ref, exactly as it does for a real
		// localFSReader project bundle — setting sourceRef directly here, as
		// this used to, pre-empted that fallback and left sourceRefTyped
		// permanently zero, which is this test double's own version of the
		// silent-withholding gap loader_version.go's bundleAtVersion had.
		if b.Name == "" {
			b.Name = ref
		}
		reads = append(reads, newRead(ref, b, prov, tctx,
			signatureFacts{signature: SignatureNone, signer: SignerNone}))
	}
	return staticReader{reads: reads}
}
