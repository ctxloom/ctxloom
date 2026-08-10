//go:build acceptance

// J001600: "a signature somebody can check" (j001600_signing.feature) — the PRODUCTION
// half of ctxloom's signing story, which no acceptance scenario had ever
// driven. J001500 proves CONSUMPTION (tamper, retraction, key revocation, rejection
// beating trust) beautifully, but against fixtures signed IN GO by
// testenv.TestSigner, sometimes with the trust root written straight to disk by
// TrustSigner. Before this file, `ctxloom bundle sign` had never produced a
// byte in an acceptance run: if key discovery regressed, if the `.sig` sibling
// were written empty, or if `--all` silently signed nothing, every existing
// trust scenario would still have passed.
//
// SEAM WITH J001500 (docs/journey-coverage-gaps.md §J001600, "one thing to decide"):
// J001500 owns the ADVERSARY. J001600 owns the PRODUCTION of the artifacts J001500 assumes —
// the signature, the trust root, the stores on disk, the relocation. Nothing
// here re-proves tamper detection.
//
// ISOLATION. Every path in this file goes through testenv.TestEnvironment,
// whose isolatedEnv() roots HOME/USERPROFILE/XDG at e.HomeDir and drops the
// canonical testsupport.EnvKeys set (mcpclient.go's sessionEnvKeys is built
// from that one list) — the subprocess analogue of testsupport.Isolate, and
// the reason `w.env.HomeFileExists(".ctxloom/allowed_signers")` is a
// MEANINGFUL assertion rather than a read of the developer's real home. No
// step here sets HOME, USERPROFILE or XDG_* itself; the one variable J001600 does
// set is SSH_AUTH_SOCK, and it is set to an absolute path this harness minted
// under w.env.Root, never to anything inherited. Repository-local git config
// goes through testenv.GitConfigLocal, which runs git under that same isolated
// environment.
//
// SSH_AUTH_SOCK AND J001500. steps_j001500.go:473-478 deliberately BLANKS SSH_AUTH_SOCK
// so an ambient developer agent cannot satisfy key discovery and turn its
// "no signing key anywhere" scenario into a host-dependent coin flip. J001600 does
// the mirror image: it points SSH_AUTH_SOCK at a hermetic in-process agent so
// key discovery deterministically SUCCEEDS. The two coexist because both are
// per-scenario writes through TestEnvironment.SetEnv, and each scenario gets a
// fresh TestEnvironment whose Cleanup restores the value that was there before
// it ran. Neither journey reads the variable it did not set, and neither can
// leave a value behind for the other: J001500 still gets its guaranteed-empty
// agent, J001600 still gets its guaranteed-present one.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/attest"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

const (
	// j001600KeyComment is the ssh-agent COMMENT Trent's key is loaded under —
	// the identity string a human recognizes, and the one
	// agentkey.resolveByComment matches for `--key <name>`.
	j001600KeyComment = "trent@acme.example"
	// j001600Principal is the allowed_signers principal Trent's team trusts.
	// Deliberately NOT the agent comment: the principal is an arbitrary
	// label (spec §7.1) with no relationship to the key's own comment, and
	// a fixture that made them identical could not tell the two apart.
	j001600Principal = "context@acme.example"
	// j001600PubKeyFile is where Trent keeps the public half, inside his
	// project — the path both `git config user.signingkey` and
	// `signer trust --key` are pointed at.
	j001600PubKeyFile = "acme-publish.pub"

	// Distinctive payload markers, so a materialized context is checked for
	// exactly the right bytes rather than an exit code or a file-exists proxy.
	j001600TDDMarker     = "J001600-TDD-GUIDANCE-MARKER"
	j001600TDDRevised    = "J001600-TDD-REVISED-MARKER"
	j001600CurlMarker    = "J001600-CURL-PIPE-SH-MARKER"
	j001600PublishedName = "secure-coding"
)

// j001600BundlesDir is the authored (committed, publishable) bundle tree — the set
// `bundle sign --all` signs and `bundle move` relocates out of.
const j001600BundlesDir = ".ctxloom/content/bundles"

// j001600State is this journey's fixture state.
type j001600State struct {
	signer    *testenv.TestSigner
	stopAgent func() error
	ready     bool

	// remote bookkeeping for the consumption scenarios
	bare       string // bare repo path (no file:// prefix)
	url        string
	referenced bool
	// publishedDir holds a copy of exactly what was published (bundle bytes
	// + the CLI-produced .sig), outside the project, so a scenario can verify
	// the published pair off disk after the authoring copy has been handed
	// off. See j001600SeedFromDisk for why the authoring copy cannot stay.
	publishedDir string

	// sharedDir is `bundle move`'s destination: a directory OUTSIDE the
	// project, so a move is a real relocation rather than a rename.
	sharedDir string
	// movedSource is the source bundle's exact bytes, captured immediately
	// before `bundle move` runs, so the destination can be compared against
	// what was really there rather than against a re-render of it.
	movedSource string

	// declared is the key the REPO says may publish its bundles (the identity
	// named in its allowed_signers), and declaredPrincipal the label it is
	// named under. Deliberately a separate signer from st.signer for the
	// refusal scenario: "the key that would sign" and "the key the repo
	// authorises" being the same value is exactly the condition under which
	// the wrong-key defect is invisible.
	declared          *testenv.TestSigner
	declaredPrincipal string

	// storeSnapshot is the project allowed_signers file's exact bytes,
	// captured before a `signer untrust` that is expected to change
	// nothing. "Reported no entry" and "left the trust root alone" are
	// different claims: a remove that rewrites the store while reporting
	// nothing removed has still edited the trust root, and only a
	// byte-for-byte comparison against what was there can see it.
	storeSnapshot string
}

func j001600Of(w *World) *j001600State {
	if w.j001600 == nil {
		w.j001600 = &j001600State{}
	}
	return w.j001600
}

// j001600Fragment is one authored fragment: name plus the marker string that IS
// its content.
type j001600Fragment struct{ name, content string }

// j001600BundleYAML renders an authored bundle manifest carrying frags in the
// given ORDER — ordered rather than map-ranged because these exact bytes are
// what gets signed, and a signature over nondeterministically-ordered YAML
// would make every re-render a spurious "the bundle changed after signing".
func j001600BundleYAML(frags ...j001600Fragment) string {
	var b strings.Builder
	b.WriteString("version: \"1.0.0\"\nfragments:\n")
	for _, f := range frags {
		fmt.Fprintf(&b, "  %s:\n    content: %q\n", f.name, f.content)
	}
	return b.String()
}

// j001600BundlePath is the authored bundle file's project-relative path.
func j001600BundlePath(name string) string { return j001600BundlesDir + "/" + name + ".yaml" }

// j001600VerifyDetachedSignature is THE assertion this journey exists for: read
// the bundle bytes and the `.sig` sibling FRESH OFF DISK and verify the pair
// with internal/signing's own verifier against Trent's public key — never by
// trusting ctxloom's own "signed by ..." success line, which is printed before
// anyone has checked anything.
//
// The empty check is separate and first on purpose: a zero-byte `.sig` is this
// codebase's characteristic bug (exit 0, a success message, no payload), and
// it must produce a message that names THAT rather than a generic parse error.
func j001600VerifyDetachedSignature(w *World, bundleName string) error {
	rel := j001600BundlePath(bundleName)
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read signed bundle %s: %w", rel, err)
	}
	sigPath := rel + ".sig"
	sig, err := w.env.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("read detached signature %s (ctxloom reported:\n%s): %w", sigPath, w.env.LastOutput(), err)
	}
	if strings.TrimSpace(sig) == "" {
		return fmt.Errorf("%s exists but is EMPTY (%d bytes) — ctxloom reported a successful signature and wrote no signature; output was:\n%s",
			sigPath, len(sig), w.env.LastOutput())
	}
	if err := signing.Verify([]byte(body), []byte(sig), j001600Of(w).signer.Public, signing.NamespacePublish); err != nil {
		return fmt.Errorf("%s does not verify against the %d bytes of %s read fresh off disk, under Trent's key %s: %w",
			sigPath, len(body), rel, j001600Of(w).signer.Fingerprint(), err)
	}
	return nil
}

// j001600DeclaredSignersPath is where a repository declares WHO may publish it:
// the ssh-keygen allowed_signers file every git-signing setup already knows,
// at the location ctxloom's own publishing repos use and CI verifies against
// (.github/verify-signatures.sh reads this exact file).
const j001600DeclaredSignersPath = ".github/allowed_signers"

// j001600DeclarePublisher writes that declaration: principal, the publish
// namespace, and pub's key line — byte-for-byte the shape of
// ctxloom-default's own .github/allowed_signers.
func j001600DeclarePublisher(w *World, principal string, key *testenv.TestSigner) error {
	st := j001600Of(w)
	st.declared = key
	st.declaredPrincipal = principal
	line := fmt.Sprintf("%s namespaces=%q %s", principal, signing.NamespacePublish,
		strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.Public))))
	return w.env.WriteFile(j001600DeclaredSignersPath, line+"\n")
}

// j001600DirBundleDir / j001600DirBundleManifest name a DIRECTORY-form bundle's tree
// and its manifest — the shape whose detached signature was never refreshed.
func j001600DirBundleDir(name string) string      { return j001600BundlesDir + "/" + name }
func j001600DirBundleManifest(name string) string { return j001600DirBundleDir(name) + "/bundle.yaml" }

// j001600VerifyDirectorySignature is defect B's assertion: the detached signature
// beside a directory bundle's manifest must cover the manifest's CURRENT bytes.
//
// It reads both fresh off disk after the edit, so a signature produced by an
// earlier signing of the same bundle — which is precisely what `bundle sign`
// used to leave behind — cannot satisfy it.
func j001600VerifyDirectorySignature(w *World, name string) error {
	rel := j001600DirBundleManifest(name)
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read signed manifest %s: %w", rel, err)
	}
	sigPath := rel + ".sig"
	sig, err := w.env.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("no detached signature at %s — `bundle sign` signed the tree and left the manifest's own "+
			"sibling untouched, so anything reading it (bundles' localFSReader, a publishing repo's CI) sees an "+
			"unsigned or stale bundle. ctxloom reported:\n%s\nerror: %w", sigPath, w.env.LastOutput(), err)
	}
	if strings.TrimSpace(sig) == "" {
		return fmt.Errorf("%s exists but is EMPTY (%d bytes); ctxloom reported:\n%s", sigPath, len(sig), w.env.LastOutput())
	}
	if err := signing.Verify([]byte(body), []byte(sig), j001600Of(w).signer.Public, signing.NamespacePublish); err != nil {
		return fmt.Errorf("%s does not cover the %d bytes of %s read fresh off disk under Trent's key %s — the signature "+
			"is STALE, which reads downstream as tampering: %w", sigPath, len(body), rel, j001600Of(w).signer.Fingerprint(), err)
	}
	return nil
}

// j001600VerifyTreeAttestation runs the CONSUMER's verifier over a directory
// bundle: attest.VerifyBundle, the same call config.loadTreeBundle makes for
// every pulled tree, against a trust root holding Trent's key.
//
// It is what makes the sibling signature's WRITE ORDER assertable. The sibling
// lives at the bundle root, which content.ManifestCovers includes, so a sibling
// written after the manifest was built is a file the manifest never claims —
// VerifyContents reports it Unclaimed and the bundle reads as content-added.
// The sibling's own signature verifies perfectly in that world, so nothing but
// this assertion can tell the two orderings apart.
func j001600VerifyTreeAttestation(w *World, name string) error {
	dir := filepath.Join(w.env.ProjectDir, filepath.FromSlash(j001600DirBundleDir(name)))
	store, err := content.NewTreeStore(afero.NewOsFs(), filepath.Dir(dir), content.Provenance{IsLocal: true})
	if err != nil {
		return fmt.Errorf("open the bundle tree at %s: %w", dir, err)
	}
	ctx := context.Background()
	tree, err := store.Open(ctx, content.BundleID(name))
	if err != nil {
		return fmt.Errorf("open the bundle tree %s: %w", name, err)
	}
	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{j001600Principal},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  j001600Of(w).signer.Public,
		KeyType:    j001600Of(w).signer.Public.Type(),
	})
	verdict, err := attest.VerifyBundle(ctx, tree, root, time.Now())
	if err != nil {
		return fmt.Errorf("verify the bundle tree %s: %w", name, err)
	}
	if verdict.Contents != nil {
		return fmt.Errorf("the tree of %s does not match the manifest just signed over it: %v — a signature written "+
			"AFTER the manifest was built is a file the manifest never claims, and every consumer reads that as "+
			"content having been added to a signed bundle", name, verdict.Contents)
	}
	if !verdict.OK() {
		return fmt.Errorf("the freshly signed bundle %s verifies as %q (%s), not as attested by its publisher",
			name, verdict.Status, verdict.Detail)
	}
	return nil
}

// j001600ListSignatures returns every `.sig` file under the authored bundle tree,
// project-relative and sorted, so a scenario can assert a COUNT.
func j001600ListSignatures(w *World) ([]string, error) {
	dir := filepath.Join(w.env.ProjectDir, filepath.FromSlash(j001600BundlesDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", j001600BundlesDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sig") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// j001600ListBundles returns every authored bundle file name (without the .yaml).
func j001600ListBundles(w *World) ([]string, error) {
	dir := filepath.Join(w.env.ProjectDir, filepath.FromSlash(j001600BundlesDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", j001600BundlesDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			out = append(out, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	return out, nil
}

// j001600Setup is the Background: a hermetic publisher's machine. Deliberately
// scaffolds config.yaml WITHOUT steps_j000200_common.go's seed bundle+profile —
// "sign --all found nothing to sign" is one of this journey's scenarios, and a
// scaffold that always authors a bundle would make that case unreachable.
func j001600Setup(w *World) error {
	st := j001600Of(w)
	if st.ready {
		return nil
	}
	if err := w.env.InitGitRepo(); err != nil {
		return err
	}
	if err := w.env.WriteFile(".ctxloom/config.yaml", buildJ000200Config("claude-code", "claude-code")); err != nil {
		return err
	}
	// The authored bundle tree exists but is empty: GetBundleDirs stats it, and
	// "the directory is missing" and "the directory holds nothing" are
	// different diagnostics from `sign --all`.
	if err := os.MkdirAll(filepath.Join(w.env.ProjectDir, filepath.FromSlash(j001600BundlesDir)), 0o755); err != nil {
		return fmt.Errorf("create authored bundles dir: %w", err)
	}

	signer, err := testenv.GenerateTestSigner()
	if err != nil {
		return fmt.Errorf("generate Trent's key: %w", err)
	}
	st.signer = signer
	if err := w.env.WriteFile(j001600PubKeyFile, signer.AuthorizedKey(j001600KeyComment)); err != nil {
		return fmt.Errorf("write Trent's public key: %w", err)
	}

	sock, stop, err := testenv.StartSSHAgent(w.env.Root, testenv.SSHAgentIdentity{Signer: signer, Comment: j001600KeyComment})
	if err != nil {
		return fmt.Errorf("start hermetic ssh-agent: %w", err)
	}
	st.stopAgent = stop
	// The ONLY environment variable this journey sets, pointed at an absolute
	// path minted above — never at anything inherited. See the file doc for how
	// this coexists with steps_j001500.go's deliberate blanking of the same key.
	w.env.SetEnv("SSH_AUTH_SOCK", sock)

	// Step 2 of agentkey's chain: an ordinary developer who already signs his
	// commits with SSH has this set, and expects tools to find it without being
	// told. Repository-local, through the harness's isolated git environment.
	if err := w.env.GitConfigLocal("user.signingkey", filepath.Join(w.env.ProjectDir, j001600PubKeyFile)); err != nil {
		return err
	}

	st.sharedDir = filepath.Join(w.env.Root, "shared-standards")
	if err := os.MkdirAll(st.sharedDir, 0o755); err != nil {
		return fmt.Errorf("create shared standards dir: %w", err)
	}

	st.ready = true
	return nil
}

// j001600PublishedBundle writes Trent's flagship bundle: two fragments, each
// carrying its own marker, so the accept scenario and the reject scenario can
// act on DIFFERENT items of the same signed bundle.
func j001600PublishedBundle(tddContent string) string {
	return j001600BundleYAML(
		j001600Fragment{name: "tdd", content: tddContent},
		j001600Fragment{name: "curl-pipe-sh", content: j001600CurlMarker},
	)
}

// j001600SeedFromDisk publishes what is ON DISK — the authored bundle bytes and
// the `.sig` `ctxloom bundle sign` itself just wrote — into a seeded git
// remote. This is what makes the consumption scenarios test the real thing:
// the signature a consumer verifies was produced by the CLI, not by
// signing.Sign in Go.
//
// It then HANDS THE BUNDLE OFF: the authoring copy is archived outside the
// project and removed from the authored tree. That is not tidiness — this one
// hermetic project plays both Trent's publishing checkout and Alice's
// consuming one, and a LOCAL authored bundle of the same name shadows the
// remote one (canonicalizeBundleArg's documented rule: "a local file of the
// same spelling still wins"). With both present, the item ref a consumer
// accepts or rejects and the item the exposure path actually delivers are two
// different things, and every per-item trust assertion silently measures
// nothing. Measured: with the authoring copy left in place, `bundle trust`
// released nothing and `bundle reject` withheld nothing, both while exiting 0.
func j001600SeedFromDisk(w *World, name string) error {
	st := j001600Of(w)
	rel := j001600BundlePath(name)
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read authored bundle to publish: %w", err)
	}
	sig, err := w.env.ReadFile(rel + ".sig")
	if err != nil {
		return fmt.Errorf("read CLI-produced signature to publish: %w", err)
	}
	files := map[string]string{rel: body, rel + ".sig": sig}

	if st.publishedDir == "" {
		st.publishedDir = filepath.Join(w.env.Root, "published")
		if err := os.MkdirAll(st.publishedDir, 0o755); err != nil {
			return fmt.Errorf("create published archive dir: %w", err)
		}
	}
	for suffix, content := range map[string]string{".yaml": body, ".yaml.sig": sig} {
		if err := os.WriteFile(filepath.Join(st.publishedDir, name+suffix), []byte(content), 0o644); err != nil {
			return fmt.Errorf("archive published %s%s: %w", name, suffix, err)
		}
	}
	for _, p := range []string{rel, rel + ".sig"} {
		if err := os.Remove(filepath.Join(w.env.ProjectDir, filepath.FromSlash(p))); err != nil {
			return fmt.Errorf("hand off authored %s: %w", p, err)
		}
	}

	if st.bare == "" {
		url, serr := w.env.SeedRemote(files)
		if serr != nil {
			return fmt.Errorf("seed company remote: %w", serr)
		}
		st.url = url
		st.bare = strings.TrimPrefix(url, "file://")
		return nil
	}
	return w.env.AdvanceRemote(st.bare, files)
}

// j001600VerifyPublished verifies the archived PUBLISHED pair — the exact bytes
// and the exact CLI-produced signature that went to the remote — read fresh
// off disk, under Trent's key.
func j001600VerifyPublished(w *World, name string) error {
	st := j001600Of(w)
	if st.publishedDir == "" {
		return fmt.Errorf("nothing has been published yet")
	}
	bundlePath := filepath.Join(st.publishedDir, name+".yaml")
	sigPath := bundlePath + ".sig"
	body, err := os.ReadFile(bundlePath)
	if err != nil {
		return fmt.Errorf("read published bundle: %w", err)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("read published signature: %w", err)
	}
	if len(strings.TrimSpace(string(sig))) == 0 {
		return fmt.Errorf("%s is EMPTY — the bundle was published with a signature file carrying nothing", sigPath)
	}
	if err := signing.Verify(body, sig, st.signer.Public, signing.NamespacePublish); err != nil {
		return fmt.Errorf("the published signature does not verify against the %d published bytes under Trent's key %s: %w",
			len(body), st.signer.Fingerprint(), err)
	}
	return nil
}

// j001600Reference wires the company remote into the consuming project the way a
// developer does — remote add (an address; never trust), reference it from the
// composed profile, then pull.
func j001600Reference(w *World) error {
	st := j001600Of(w)
	if st.referenced {
		return runOK(w, "deps", "pull")
	}
	if st.url == "" {
		return fmt.Errorf("Trent has not published his bundle yet")
	}
	if err := runOK(w, "bundle", "create", "seed", "-d", "J001600 seed bundle"); err != nil {
		return err
	}
	if err := runOK(w, "profile", "create", "default", "-b", "seed", "-d", "J001600 default profile"); err != nil {
		return err
	}
	if err := runOK(w, "remote", "create", "company", st.url, "--forge", "git"); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", "default", "--add-bundle", "company/"+j001600PublishedName); err != nil {
		return err
	}
	st.referenced = true
	return runOK(w, "deps", "pull")
}

// j001600ItemRef builds the canonical item ref for a fragment of the PUBLISHED
// bundle — the same grammar `bundle trust`, `bundle reject` and `bundle sign`
// all share.
func j001600ItemRef(w *World, fragment string) string {
	return "file://" + j001600Of(w).bare + "@bundles/" + j001600PublishedName + "#fragments/" + fragment
}

// j001600Delivered materializes the default profile and returns the assembled
// context — the surface a scenario asserts a marker reached, or did not.
func j001600Delivered(w *World) (string, error) {
	_ = w.env.Run("profile", "materialize", "default", "--target", "out")
	body, err := w.env.ReadFile(filepath.Join("out", "CLAUDE.md"))
	if err != nil {
		return "", fmt.Errorf("read materialized out/CLAUDE.md (materialize output:\n%s): %w", w.env.LastOutput(), err)
	}
	w.docStepMaterialized = body
	return body, nil
}

// j001600AssertDelivery checks a marker's presence in the assembled context.
func j001600AssertDelivery(w *World, marker string, want bool) error {
	body, err := j001600Delivered(w)
	if err != nil {
		return err
	}
	has := strings.Contains(body, marker)
	if want && !has {
		return fmt.Errorf("the assembled context does not carry %q; delivered:\n%s", marker, body)
	}
	if !want && has {
		return fmt.Errorf("the assembled context still carries %q, which should have been withheld; delivered:\n%s", marker, body)
	}
	return nil
}

// j001600AssertReviewState reads the review-state LABEL `ctxloom fragment list
// --format json` renders for a fragment of the PUBLISHED bundle — the same
// operations.TrustStamper/EffectiveTrust verdict materialize applies, surfaced
// as the word a human sees in `ctxloom review`.
//
// This is the half of "a later revision returns it to review" that an ABSENCE
// cannot fake. Withheld-because-the-approval-no-longer-covers-these-bytes and
// withheld-because-the-revision-never-arrived look identical in the delivered
// payload; they read differently here, because bytes that never arrived leave
// the original sitting at "accepted".
func j001600AssertReviewState(w *World, fragment, want string) error {
	if err := runOK(w, "fragment", "list", "--format", "json"); err != nil {
		return err
	}
	out := w.env.LastOutput()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return fmt.Errorf("parse `fragment list --format json`: %w\noutput:\n%s", err, out)
	}
	for _, row := range rows {
		if n, _ := row["name"].(string); n != fragment {
			continue
		}
		// bundle_label, not bundle: for remote content "bundle" is the whole
		// canonical "<url>@bundles/<name>" ref, and the label is the bare name.
		// The pairing matters — the SAME fragment name also arrives from the
		// companion bundles this machine happens to have installed.
		if b, _ := row["bundle_label"].(string); b != j001600PublishedName {
			continue
		}
		got, _ := row["state"].(string)
		w.docStepMaterialized = fmt.Sprintf("fragment list --format json → %q: state=%q trust_source=%v trusted=%v",
			fragment, got, row["trust_source"], row["trusted"])
		if got != want {
			return fmt.Errorf("the published %q fragment's review state is %q, want %q — a revision the earlier acceptance "+
				"does not cover must come back for review, not stay decided; row: %v", fragment, got, want, row)
		}
		return nil
	}
	return fmt.Errorf("no %q fragment of bundle %q in `fragment list --format json`:\n%s", fragment, j001600PublishedName, out)
}

// j001600EmbeddedPrincipals returns the principals ctxloom's compiled-in trust
// root names, read from the SAME embedded bytes the product reads rather than
// hard-coded here. A literal would rot the moment the release key rotates,
// and would rot SILENTLY: the scenario would go on asserting a suppression of
// a principal nothing trusts any more, which passes for the wrong reason.
func j001600EmbeddedPrincipals() []string {
	var out []string
	for _, e := range config.EmbeddedSigners().Entries() {
		out = append(out, e.Principals...)
	}
	return out
}

// j001600StoreEntry is one parsed allowed_signers line.
type j001600StoreEntry struct {
	principal  string
	namespaces string
	pub        ssh.PublicKey
}

// j001600ParseStore parses an allowed_signers file into entries, so an assertion
// can name the KEY (by fingerprint) and the NAMESPACE rather than
// substring-matching the principal and calling that a trust root.
func j001600ParseStore(body string) ([]j001600StoreEntry, error) {
	var out []j001600StoreEntry
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("allowed_signers line is not <principals> <options?> <key>: %q", line)
		}
		entry := j001600StoreEntry{principal: fields[0]}
		rest := strings.TrimSpace(fields[1])
		if strings.HasPrefix(rest, "namespaces=") {
			nsAndKey := strings.SplitN(rest, " ", 2)
			if len(nsAndKey) != 2 {
				return nil, fmt.Errorf("allowed_signers line has options but no key: %q", line)
			}
			entry.namespaces = strings.Trim(strings.TrimPrefix(nsAndKey[0], "namespaces="), `"`)
			rest = strings.TrimSpace(nsAndKey[1])
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(rest))
		if err != nil {
			return nil, fmt.Errorf("allowed_signers line carries an unparseable key (%q): %w", line, err)
		}
		entry.pub = pub
		out = append(out, entry)
	}
	return out, nil
}

// j001600AssertTrustRoot asserts that body (an allowed_signers file's contents)
// names principal, for TRENT'S actual key, in the publish namespace.
//
// The namespace check is EXACT, not a substring. A grant is the set of things
// a key may do, so "the publish namespace appears somewhere in this field"
// passes just as happily on `namespaces="publish.v1.ctxloom.dev"` as on
// `namespaces=",publish.v1.ctxloom.dev"` or on a field that silently carries
// approve as well — and the difference between those is the difference
// between the trust root a human authorised and a wider one. Compare the
// whole field against exactly the namespaces expected.
func j001600AssertTrustRoot(w *World, label, body, principal string, wantNamespaces ...string) error {
	if len(wantNamespaces) == 0 {
		wantNamespaces = []string{signing.NamespacePublish}
	}
	entries, err := j001600ParseStore(body)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	want := j001600Of(w).signer.Fingerprint()
	for _, e := range entries {
		if e.principal != principal {
			continue
		}
		got := ssh.FingerprintSHA256(e.pub)
		if got != want {
			return fmt.Errorf("%s trusts %s for key %s, but Trent's key is %s", label, principal, got, want)
		}
		if e.namespaces != strings.Join(wantNamespaces, ",") {
			return fmt.Errorf("%s trusts %s for namespaces %q, but the grant authorised was exactly %q — a trust root that grants more (or is malformed) than what was asked for is not the one the operator approved",
				label, principal, e.namespaces, strings.Join(wantNamespaces, ","))
		}
		return nil
	}
	return fmt.Errorf("%s has no entry for %s at all; contents:\n%s", label, principal, body)
}

func registerJ001600Steps(ctx *godog.ScenarioContext) {
	// The in-process ssh-agent's listener is this journey's only long-lived
	// resource. Stopping it here (rather than leaning on temp-dir removal)
	// means the accept loop and every connection goroutine are joined before
	// the scenario is declared over — no leaked goroutine, no stray socket.
	ctx.After(func(c context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		w := worldFrom(c)
		if w == nil || w.j001600 == nil || w.j001600.stopAgent == nil {
			return c, nil
		}
		stop := w.j001600.stopAgent
		w.j001600.stopAgent = nil
		if serr := stop(); serr != nil {
			return c, fmt.Errorf("stop hermetic ssh-agent: %w", serr)
		}
		return c, nil
	})

	// --- Background ---------------------------------------------------------

	ctx.Step(`^Trent's signing key is in his ssh-agent, and git already knows it is his signing key$`, func(c context.Context) error {
		return j001600Setup(worldFrom(c))
	})

	// --- Authoring fixtures -------------------------------------------------

	ctx.Step(`^Trent has authored no bundles yet$`, func(c context.Context) error {
		w := worldFrom(c)
		names, err := j001600ListBundles(w)
		if err != nil {
			return err
		}
		if len(names) != 0 {
			return fmt.Errorf("expected an empty publish set, found %v", names)
		}
		return nil
	})

	ctx.Step(`^Trent's project publishes a bundle "([^"]*)" carrying the fragment "([^"]*)"$`, func(c context.Context, name, frag string) error {
		w := worldFrom(c)
		return w.env.WriteFile(j001600BundlePath(name), j001600BundleYAML(j001600Fragment{name: frag, content: j001600TDDMarker}))
	})

	ctx.Step(`^Trent's project publishes the "([^"]*)" bundle his team depends on$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		return w.env.WriteFile(j001600BundlePath(name), j001600PublishedBundle(j001600TDDMarker))
	})

	ctx.Step(`^Trent's project publishes (\d+) bundles$`, func(c context.Context, n int) error {
		w := worldFrom(c)
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("standard-%d", i)
			body := j001600BundleYAML(j001600Fragment{name: "guidance", content: fmt.Sprintf("%s-%d", j001600TDDMarker, i)})
			if err := w.env.WriteFile(j001600BundlePath(name), body); err != nil {
				return err
			}
		}
		return nil
	})

	ctx.Step(`^Trent has signed the bundle "([^"]*)"$`, func(c context.Context, name string) error {
		return runOK(worldFrom(c), "bundle", "sign", name)
	})

	// A DIRECTORY-form bundle: <name>/bundle.yaml, the shape that can also ship
	// skills, and the one whose detached signature `bundle sign` never
	// refreshed.
	ctx.Step(`^Trent's project publishes the directory bundle "([^"]*)" carrying the fragment "([^"]*)"$`, func(c context.Context, name, frag string) error {
		w := worldFrom(c)
		return w.env.WriteFile(j001600DirBundleManifest(name), j001600BundleYAML(j001600Fragment{name: frag, content: j001600TDDMarker}))
	})

	// Edited IN PLACE, not through `ctxloom bundle modify`: this is the
	// publisher's real motion (open the YAML, change the guidance, re-sign), and
	// it is what leaves a signature covering bytes that are no longer there.
	ctx.Step(`^Trent revises the directory bundle "([^"]*)"$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		rel := j001600DirBundleManifest(name)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return err
		}
		return w.env.WriteFile(rel, body+"  revised:\n    content: \"revised after the first signature\"\n")
	})

	// --- Who the repo authorises to publish it -------------------------------

	ctx.Step(`^Trent's repo declares "([^"]*)" as the only identity allowed to publish, holding a key Trent does not have$`, func(c context.Context, principal string) error {
		w := worldFrom(c)
		release, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate the release identity's key: %w", err)
		}
		if release.Fingerprint() == j001600Of(w).signer.Fingerprint() {
			return fmt.Errorf("the release key and Trent's key came out identical; this fixture is meaningless unless they differ")
		}
		return j001600DeclarePublisher(w, principal, release)
	})

	ctx.Step(`^Trent's repo declares "([^"]*)" as the only identity allowed to publish, and it is Trent's own key$`, func(c context.Context, principal string) error {
		w := worldFrom(c)
		return j001600DeclarePublisher(w, principal, j001600Of(w).signer)
	})

	// BOTH halves, because a refusal naming only one leaves the publisher
	// guessing: WHICH key was about to sign (there may be several in the agent,
	// and the one git points at is not always the one intended), and WHICH
	// identity the repo actually requires.
	ctx.Step(`^the refusal names the key it would have signed with and the principal the repo requires$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j001600Of(w)
		out := w.env.LastOutput()
		for _, want := range []struct{ what, text string }{
			{"the key it would have signed with", st.signer.Fingerprint()},
			{"the principal the repo requires", st.declaredPrincipal},
			{"where that requirement is declared", j001600DeclaredSignersPath},
		} {
			if !strings.Contains(out, want.text) {
				return fmt.Errorf("the refusal does not name %s (%q); ctxloom said:\n%s", want.what, want.text, out)
			}
		}
		w.docStepMaterialized = out
		return nil
	})

	// The .github/verify-signatures.sh check, in Go: every signature in the tree
	// must verify against the DECLARED key in the publish namespace. An empty
	// sweep fails rather than passing vacuously — a check that verified nothing
	// is indistinguishable from one that verified everything.
	ctx.Step(`^every signature in the published bundle tree verifies against the key the repo declares$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j001600Of(w)
		if st.declared == nil {
			return fmt.Errorf("no publisher identity has been declared by this fixture")
		}
		bundles, err := j001600ListBundles(w)
		if err != nil {
			return err
		}
		if len(bundles) == 0 {
			return fmt.Errorf("no published bundles at all — refusing to report a verification that checked nothing")
		}
		for _, name := range bundles {
			rel := j001600BundlePath(name)
			body, rerr := w.env.ReadFile(rel)
			if rerr != nil {
				return fmt.Errorf("read %s: %w", rel, rerr)
			}
			sig, serr := w.env.ReadFile(rel + ".sig")
			if serr != nil {
				return fmt.Errorf("no signature beside %s (ctxloom reported:\n%s): %w", rel, w.env.LastOutput(), serr)
			}
			if verr := signing.Verify([]byte(body), []byte(sig), st.declared.Public, signing.NamespacePublish); verr != nil {
				return fmt.Errorf("%s.sig does not verify under %s, the key %s declares for %s — this is exactly what a "+
					"consumer sees before it withholds the bundle: %w",
					rel, ssh.FingerprintSHA256(st.declared.Public), j001600DeclaredSignersPath, st.declaredPrincipal, verr)
			}
		}
		return nil
	})

	ctx.Step(`^the signature beside the directory bundle "([^"]*)" verifies against its bundle\.yaml on disk$`, func(c context.Context, name string) error {
		return j001600VerifyDirectorySignature(worldFrom(c), name)
	})

	ctx.Step(`^the directory bundle "([^"]*)" still verifies as a whole tree, with nothing unclaimed$`, func(c context.Context, name string) error {
		return j001600VerifyTreeAttestation(worldFrom(c), name)
	})

	ctx.Step(`^Trent edits the bundle "([^"]*)" after signing it$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(j001600BundlePath(name))
		if err != nil {
			return err
		}
		return w.env.WriteFile(j001600BundlePath(name), body+"  late-addition:\n    content: \"added after the signature\"\n")
	})

	// --- Signature payload assertions ---------------------------------------

	ctx.Step(`^the signature beside bundle "([^"]*)" is non-empty and verifies against the bundle's bytes on disk$`, func(c context.Context, name string) error {
		return j001600VerifyDetachedSignature(worldFrom(c), name)
	})

	ctx.Step(`^every published bundle carries a signature that verifies, and there are exactly (\d+)$`, func(c context.Context, want int) error {
		w := worldFrom(c)
		bundles, err := j001600ListBundles(w)
		if err != nil {
			return err
		}
		sigs, err := j001600ListSignatures(w)
		if err != nil {
			return err
		}
		if len(sigs) != want {
			return fmt.Errorf("expected exactly %d signatures, found %d (%v) for %d published bundles (%v); ctxloom reported:\n%s",
				want, len(sigs), sigs, len(bundles), bundles, w.env.LastOutput())
		}
		if len(bundles) != want {
			return fmt.Errorf("expected exactly %d published bundles to sign, found %d (%v)", want, len(bundles), bundles)
		}
		for _, name := range bundles {
			if err := j001600VerifyDetachedSignature(w, name); err != nil {
				return err
			}
		}
		return nil
	})

	ctx.Step(`^exactly (\d+) signature files? exists? in the published bundle tree$`, func(c context.Context, want int) error {
		w := worldFrom(c)
		sigs, err := j001600ListSignatures(w)
		if err != nil {
			return err
		}
		if len(sigs) != want {
			return fmt.Errorf("expected exactly %d signature file(s), found %d: %v", want, len(sigs), sigs)
		}
		return nil
	})

	// --- Trust store assertions ---------------------------------------------

	ctx.Step(`^Trent's key is trusted in the committable project store as "([^"]*)"$`, func(c context.Context, principal string) error {
		return runOK(worldFrom(c), "signer", "trust", principal, "--key", j001600PubKeyFile, "--project", "--yes")
	})

	ctx.Step(`^Trent's key is also trusted in his personal user store as "([^"]*)"$`, func(c context.Context, principal string) error {
		return runOK(worldFrom(c), "signer", "trust", principal, "--key", j001600PubKeyFile, "--user", "--yes")
	})

	// A review decision recorded by someone holding a signing key is SIGNED
	// (J001500's forgery scenario proves the converse: a team decision refuses to be
	// recorded without one), and a signed decision is only honoured when its
	// signer is trusted for the approve/reject namespaces. Granting that here
	// is what makes the accept/reject scenarios measure the decision rather
	// than measure a trust gap.
	//
	// This hermetic world holds ONE ssh-agent identity, so the same key plays
	// the publisher and the reviewer. That is not a shortcut being papered
	// over — it is the seam worth showing: the store distinguishes the two
	// roles by NAMESPACE, not by key, so `context@…` (publish) and
	// `reviewer@…` (approve, reject) are separate grants over the same bytes.
	//
	// PRODUCT BUG FOUND HERE, reported not fixed — see the @wip scenario in
	// j001600_signing.feature: WITHOUT this grant, `ctxloom bundle trust` prints
	// "Approved …  signed by SHA256:…", exits 0, writes a well-formed signed
	// approval record — and the item stays withheld. The byte-identical
	// UNSIGNED record (same ref, same payload_hash) is honoured. So on the
	// ordinary developer setup — a key in ssh-agent — the flagship trust
	// command is a silent no-op.
	ctx.Step(`^Alice's own review key is trusted for approve and reject as "([^"]*)"$`, func(c context.Context, principal string) error {
		return runOK(worldFrom(c), "signer", "trust", principal, "--key", j001600PubKeyFile, "--namespace", "approve,reject", "--yes")
	})

	ctx.Step(`^the project store "([^"]*)" trusts "([^"]*)" for publishing, with Trent's own key$`, func(c context.Context, rel, principal string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read project trust store %s (ctxloom reported:\n%s): %w", rel, w.env.LastOutput(), err)
		}
		return j001600AssertTrustRoot(w, "the project store "+rel, body, principal)
	})

	ctx.Step(`^the user store "([^"]*)" was never written$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		if !w.env.HomeFileExists(rel) {
			return nil
		}
		body, err := w.env.ReadHomeFile(rel)
		if err != nil {
			return fmt.Errorf("read user trust store ~/%s: %w", rel, err)
		}
		entries, perr := j001600ParseStore(body)
		if perr != nil {
			return fmt.Errorf("user trust store ~/%s exists and is unparseable: %w", rel, perr)
		}
		want := j001600Of(w).signer.Fingerprint()
		for _, e := range entries {
			if ssh.FingerprintSHA256(e.pub) == want {
				return fmt.Errorf("--project wrote Trent's key into the USER store ~/%s as well; a trust root in the wrong store is invisible until a teammate's clone silently trusts nothing. Contents:\n%s", rel, body)
			}
		}
		return nil
	})

	ctx.Step(`^the project store "([^"]*)" no longer names Trent's key$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		if !w.env.FileExists(rel) {
			return nil
		}
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return err
		}
		entries, perr := j001600ParseStore(body)
		if perr != nil {
			return fmt.Errorf("project trust store %s is unparseable after the removal: %w", rel, perr)
		}
		want := j001600Of(w).signer.Fingerprint()
		for _, e := range entries {
			if ssh.FingerprintSHA256(e.pub) == want {
				return fmt.Errorf("the key line for %s is still in %s after `signer untrust`; contents:\n%s", want, rel, body)
			}
		}
		return nil
	})

	// --- The store as a FILE: what a removal leaves behind ------------------
	//
	// Every step below asserts allowed_signers CONTENT, because that file IS
	// the trust root. `signer untrust` reporting "removed 1 entry" is a
	// claim about one line; what makes the store still a trust root is every
	// OTHER line surviving the rewrite unchanged. The removal path rebuilds
	// the whole file from the lines it decided to keep, so a bug there is
	// silent: the command still exits 0 and still prints the right count
	// while the file it wrote has lost, duplicated, or re-joined its
	// remaining entries.

	ctx.Step(`^Trent's team trusts "([^"]*)" in the committable project store$`, func(c context.Context, principals string) error {
		w := worldFrom(c)
		for _, p := range strings.Split(principals, ",") {
			if err := runOK(w, "signer", "trust", strings.TrimSpace(p), "--key", j001600PubKeyFile, "--namespace", "publish", "--project", "--yes"); err != nil {
				return err
			}
		}
		return nil
	})

	ctx.Step(`^the project store "([^"]*)" holds exactly the entries for "([^"]*)"$`, func(c context.Context, rel, principals string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read project trust store %s (ctxloom reported:\n%s): %w", rel, w.env.LastOutput(), err)
		}
		var want []string
		for _, p := range strings.Split(principals, ",") {
			want = append(want, strings.TrimSpace(p))
		}
		entries, perr := j001600ParseStore(body)
		if perr != nil {
			return fmt.Errorf("project trust store %s is unparseable after the removal: %w", rel, perr)
		}
		var got []string
		for _, e := range entries {
			got = append(got, e.principal)
		}
		// Order and multiplicity both matter. The rewrite reassembles the
		// file from a kept-lines slice, so a dropped survivor, a duplicated
		// one, and a reordered one are all distinct ways for it to be wrong,
		// and comparing the sequence catches every one of them.
		if !slices.Equal(got, want) {
			return fmt.Errorf("project store %s holds entries for %v, want exactly %v (in that order) — the surviving lines of a trust root must outlive the removal of an unrelated one; contents:\n%s",
				rel, got, want, body)
		}
		// Each survivor must still be a USABLE grant, not merely a line that
		// happens to mention the right principal: a rewrite that mangled the
		// key or the namespace field would leave the principal readable while
		// trusting nothing (or trusting more).
		for _, p := range want {
			if aerr := j001600AssertTrustRoot(w, "the project store "+rel, body, p); aerr != nil {
				return aerr
			}
		}
		// The file must be a well-formed allowed_signers file, which means
		// exactly one line per entry and a final newline. `out += "\n"` on a
		// non-empty result is the whole of what guarantees the last entry is
		// terminated; without it the store's final line is one ssh-keygen
		// refuses to read, and the key silently stops counting.
		if !strings.HasSuffix(body, "\n") {
			return fmt.Errorf("project store %s does not end in a newline; ssh-keygen drops an unterminated final line, so the last entry silently grants nothing. Contents:\n%q", rel, body)
		}
		if n := len(strings.Split(strings.TrimSuffix(body, "\n"), "\n")); n != len(want) {
			return fmt.Errorf("project store %s has %d line(s) for %d entries — the rewrite added or lost a line; contents:\n%q", rel, n, len(want), body)
		}
		return nil
	})

	// Removing the LAST entry is the one case where the kept-lines slice is
	// empty, and "no entries" is not the same file as "one blank line": a
	// store left holding a stray newline is a trust root the rewrite decided
	// to terminate when it had nothing to terminate. Asserting emptiness
	// rather than "no longer names Trent's key" is what separates them.
	ctx.Step(`^the project store "([^"]*)" holds nothing at all$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		if !w.env.FileExists(rel) {
			return nil // removing the last entry may leave no file at all
		}
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read project trust store %s: %w", rel, err)
		}
		if body != "" {
			return fmt.Errorf("the project store %s still holds %q after its last entry was removed; an emptied trust root should be empty, not a file with a line in it", rel, body)
		}
		return nil
	})

	ctx.Step(`^the project store "([^"]*)" trusts "([^"]*)" for exactly the namespaces "([^"]*)"$`, func(c context.Context, rel, principal, namespaces string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read project trust store %s (ctxloom reported:\n%s): %w", rel, w.env.LastOutput(), err)
		}
		return j001600AssertTrustRoot(w, "the project store "+rel, body, principal, strings.Split(namespaces, ",")...)
	})

	// Hand-edited stores really do end without a newline; the fixture writes
	// one on purpose rather than hoping a previous command left one.
	ctx.Step(`^the project store "([^"]*)" holds one entry with no trailing newline$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		line := fmt.Sprintf("handwritten@acme.example namespaces=%q %s",
			signing.NamespacePublish, strings.TrimSpace(j001600Of(w).signer.AuthorizedKey(j001600KeyComment)))
		return w.env.WriteFile(rel, line) // deliberately no "\n"
	})

	ctx.Step(`^I note exactly what the project store "([^"]*)" holds$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read project trust store %s: %w", rel, err)
		}
		j001600Of(w).storeSnapshot = body
		return nil
	})

	ctx.Step(`^the project store "([^"]*)" is byte-for-byte what it was$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read project trust store %s: %w", rel, err)
		}
		if body != j001600Of(w).storeSnapshot {
			return fmt.Errorf("removing a principal the store does not hold rewrote the trust root anyway.\nbefore:\n%q\nafter:\n%q", j001600Of(w).storeSnapshot, body)
		}
		return nil
	})

	// A line the parser cannot read is the dangerous case: it contributes no
	// entry, so the principal it names LOOKS absent, and a remove that
	// cheerfully reports "no entry for X" tells an operator a key is
	// untrusted while a line they cannot see stays in the file.
	ctx.Step(`^one line in the project store "([^"]*)" cannot be read$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read project trust store %s: %w", rel, err)
		}
		return w.env.WriteFile(rel, body+"this-line-is-not-an-allowed-signers-entry\n")
	})

	ctx.Step(`^the project store "([^"]*)" still holds the line that could not be read$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read project trust store %s: %w", rel, err)
		}
		if !strings.Contains(body, "this-line-is-not-an-allowed-signers-entry") {
			return fmt.Errorf("the unreadable line was dropped from %s; a refusal must leave the trust root exactly as it found it. Contents:\n%s", rel, body)
		}
		return nil
	})

	// --- The embedded trust root: withdrawing trust in ctxloom's own key ----

	ctx.Step(`^ctxloom's own compiled-in publishing key is trusted$`, func(c context.Context) error {
		w := worldFrom(c)
		if len(j001600EmbeddedPrincipals()) == 0 {
			return fmt.Errorf("this build has no compiled-in trust root, so there is no embedded key to withdraw trust in")
		}
		return runOK(w, "signer", "list")
	})

	ctx.Step(`^the distrusted store "([^"]*)" records every principal that entry names$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read project distrusted store %s (ctxloom reported:\n%s): %w", rel, w.env.LastOutput(), err)
		}
		// The suppression the read side honours is a LITERAL membership check
		// against the embedded entry's OWN principals, so recording anything
		// other than those exact strings reports success while suppressing
		// nothing. Assert the recorded strings, not that the file is non-empty.
		var recorded []string
		for _, line := range strings.Split(body, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				recorded = append(recorded, line)
			}
		}
		want := j001600EmbeddedPrincipals()
		for _, p := range want {
			if !slices.Contains(recorded, p) {
				return fmt.Errorf("the distrusted store %s does not record %q — the read side's literal check will never match, so the withdrawal suppresses nothing. Recorded: %v", rel, p, recorded)
			}
		}
		// Idempotence is a property of the FILE, not of the exit code: a
		// second withdrawal that appends the same principal again leaves a
		// store that grows without bound and no longer says one thing once.
		seen := map[string]int{}
		for _, p := range recorded {
			seen[p]++
			if seen[p] > 1 {
				return fmt.Errorf("the distrusted store %s records %q %d times; suppression must be idempotent. Contents:\n%s", rel, p, seen[p], body)
			}
		}
		return nil
	})

	ctx.Step(`^the listing names "([^"]*)" in the "([^"]*)" store, with Trent's fingerprint and the publish namespace$`, func(c context.Context, principal, store string) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		fp := j001600Of(w).signer.Fingerprint()
		for _, line := range strings.Split(out, "\n") {
			if !strings.Contains(line, principal) {
				continue
			}
			if strings.Contains(line, store) && strings.Contains(line, fp) && strings.Contains(line, signing.NamespacePublish) {
				return nil
			}
		}
		return fmt.Errorf("no line names %q in the %q store with fingerprint %s and namespace %s; output was:\n%s",
			principal, store, fp, signing.NamespacePublish, out)
	})

	// --- Consumption: what actually reaches the assistant --------------------

	ctx.Step(`^Trent publishes the signed bundle to his company repo, and Alice references it$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001600SeedFromDisk(w, j001600PublishedName); err != nil {
			return err
		}
		return j001600Reference(w)
	})

	// A plain `deps pull` is PASSIVE by design — remote_upgrade.go's own doc:
	// "Passive 'deps pull' installs exactly what is already pinned and never
	// advances" — so on a bundle this project already installed once it reports
	// "Skipped (kept at their locked commit)" and Alice keeps the ORIGINAL
	// bytes. Taking Trent's newly published commit needs `deps check
	// --apply` first (--force skips the per-item confirmation prompt these
	// stdin-less exec.Command runs cannot answer), exactly as
	// steps_trust_surface.go's tsUpdateAndPull already documents.
	//
	// Getting this wrong is not a slow test, it is a silently vacuous one: with
	// the passive pull, "the revised guidance is not delivered" passed against
	// bytes that had never entered the project at all (audit irate-catfish, F1).
	// The scenario's two companion assertions — the ORIGINAL stops being
	// delivered, and the item reads pending again — are what make that failure
	// mode impossible to re-introduce silently.
	ctx.Step(`^Alice pulls the newly published version$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := runOK(w, "deps", "upgrade"); err != nil {
			return err
		}
		return runOK(w, "deps", "pull")
	})

	ctx.Step(`^her assistant no longer receives the "([^"]*)" guidance either$`, func(c context.Context, which string) error {
		marker, err := j001600MarkerFor(which)
		if err != nil {
			return err
		}
		return j001600AssertDelivery(worldFrom(c), marker, false)
	})

	ctx.Step(`^the published "([^"]*)" fragment's review state is "([^"]*)"$`, func(c context.Context, frag, want string) error {
		return j001600AssertReviewState(worldFrom(c), frag, want)
	})

	ctx.Step(`^Trent revises the "([^"]*)" fragment, re-signs it, and publishes again$`, func(c context.Context, frag string) error {
		w := worldFrom(c)
		if frag != "tdd" {
			return fmt.Errorf("this fixture only revises the tdd fragment, not %q", frag)
		}
		if err := w.env.WriteFile(j001600BundlePath(j001600PublishedName), j001600PublishedBundle(j001600TDDRevised)); err != nil {
			return err
		}
		if err := runOK(w, "bundle", "sign", j001600PublishedName); err != nil {
			return err
		}
		return j001600SeedFromDisk(w, j001600PublishedName)
	})

	ctx.Step(`^I run "ctxloom bundle trust" on the published "([^"]*)" fragment$`, func(c context.Context, frag string) error {
		return runOK(worldFrom(c), "bundle", "trust", j001600ItemRef(worldFrom(c), frag))
	})

	ctx.Step(`^I run "ctxloom bundle reject" on the published "([^"]*)" fragment$`, func(c context.Context, frag string) error {
		return runOK(worldFrom(c), "bundle", "reject", j001600ItemRef(worldFrom(c), frag))
	})

	// "try to run", not runOK: this scenario's whole subject is a REFUSAL, so
	// the exit code is an assertion the scenario makes explicitly rather than
	// a precondition the step swallows.
	ctx.Step(`^I try to run "ctxloom bundle trust" on the published "([^"]*)" fragment$`, func(c context.Context, frag string) error {
		w := worldFrom(c)
		_ = w.env.Run("bundle", "trust", j001600ItemRef(w, frag))
		return nil
	})

	// The message a human acts on, in three parts, because a refusal missing
	// any one of them leaves them stuck: WHICH key was refused (there may be
	// several in the agent), WHICH namespace it lacks (approve and reject are
	// separate grants over the same key), and the command that fixes it.
	// Asserted as three independent substrings so a message that drops one
	// fails naming the part it dropped.
	ctx.Step(`^the refusal names Alice's key, the "([^"]*)" namespace, and how to trust it$`, func(c context.Context, ns string) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		fp := j001600Of(w).signer.Fingerprint()
		for _, want := range []struct{ what, text string }{
			{"the key it refused", fp},
			{"the namespace that key lacks", ns},
			{"the command that grants it", "ctxloom signer trust"},
			{"the namespaces to grant", "--namespace approve,reject"},
		} {
			if !strings.Contains(out, want.text) {
				return fmt.Errorf("the refusal does not name %s (%q); a refusal a user cannot act on is barely better "+
					"than the silent success it replaced. ctxloom said:\n%s", want.what, want.text, out)
			}
		}
		w.docStepMaterialized = out
		return nil
	})

	// NOTHING WAS WRITTEN — read off the store's own files, deliberately not
	// through countersign.Store's verified lookup. That lookup answers "no"
	// for a record that WAS written by an untrusted key (which is the bug),
	// so it cannot tell a refusal from the failure being fixed. Record
	// filenames carry the assertion (`<indexHash>.<assertion>.<keyTag>.sig`
	// / `.unsigned`), and the sidecar index carries it as a field, so both
	// halves of what `bundle trust` writes are checked.
	ctx.Step(`^the approvals store holds no approve record at all$`, func(c context.Context) error {
		w := worldFrom(c)
		dir := filepath.Join(w.env.HomeDir, paths.AppDirName, paths.ApprovalsDirName)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				// The store was never even created: the strongest form of
				// "nothing was recorded".
				w.docStepMaterialized = "approvals store " + dir + ": never created — nothing was recorded"
				return nil
			}
			return fmt.Errorf("read approvals store %s: %w", dir, err)
		}
		marker := "." + string(signing.AssertionApprove) + "."
		var found []string
		for _, e := range entries {
			if !e.IsDir() && strings.Contains(e.Name(), marker) {
				found = append(found, e.Name())
			}
		}
		if len(found) > 0 {
			return fmt.Errorf("the approvals store %s holds %d approve record(s) after the refusal: %v — "+
				"refusing and writing anyway leaves exactly the record nothing honours, which is the bug being fixed",
				dir, len(found), found)
		}
		index := filepath.Join(dir, "index.yaml")
		body, rerr := os.ReadFile(index)
		if rerr != nil && !os.IsNotExist(rerr) {
			return fmt.Errorf("read approvals index %s: %w", index, rerr)
		}
		if rerr == nil {
			var rows []countersign.IndexEntry
			if err := yaml.Unmarshal(body, &rows); err != nil {
				return fmt.Errorf("parse approvals index %s: %w\ncontents:\n%s", index, err, body)
			}
			for _, row := range rows {
				if row.Assertion == string(signing.AssertionApprove) {
					return fmt.Errorf("the approvals index %s records an approval of %q after the refusal "+
						"(principal %q, unsigned=%v); the sidecar is what `ctxloom review` reads to label an item, "+
						"so an entry here tells the user a decision was made that was not",
						index, row.Ref, row.Principal, row.Unsigned)
				}
			}
		}
		w.docStepMaterialized = fmt.Sprintf("approvals store %s: %d file(s), no approve record, no approve index entry", dir, len(entries))
		return nil
	})

	ctx.Step(`^her assistant receives the "([^"]*)" guidance$`, func(c context.Context, which string) error {
		marker, err := j001600MarkerFor(which)
		if err != nil {
			return err
		}
		return j001600AssertDelivery(worldFrom(c), marker, true)
	})

	ctx.Step(`^her assistant does not receive the "([^"]*)" guidance$`, func(c context.Context, which string) error {
		marker, err := j001600MarkerFor(which)
		if err != nil {
			return err
		}
		return j001600AssertDelivery(worldFrom(c), marker, false)
	})

	ctx.Step(`^the content Trent signed still verifies against the bytes he published$`, func(c context.Context) error {
		return j001600VerifyPublished(worldFrom(c), j001600PublishedName)
	})

	// --- bundle move ---------------------------------------------------------

	ctx.Step(`^I run "ctxloom bundle move" to relocate "([^"]*)" into the shared standards directory$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		st := j001600Of(w)
		body, err := w.env.ReadFile(j001600BundlePath(name))
		if err != nil {
			return err
		}
		st.movedSource = body
		_ = w.env.Run("bundle", "move", name, "--to", st.sharedDir)
		return nil
	})

	ctx.Step(`^the relocated "([^"]*)" is byte-identical to what was signed, and its signature still verifies$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		st := j001600Of(w)
		destBundle := filepath.Join(st.sharedDir, name+".yaml")
		destSig := destBundle + ".sig"
		body, err := os.ReadFile(destBundle)
		if err != nil {
			return fmt.Errorf("read relocated bundle %s (move reported:\n%s): %w", destBundle, w.env.LastOutput(), err)
		}
		if string(body) != st.movedSource {
			return fmt.Errorf("the relocated bundle is NOT byte-identical to the source: move re-parsed or re-serialized it, which silently kills the signature.\nsource (%d bytes):\n%s\ndestination (%d bytes):\n%s",
				len(st.movedSource), st.movedSource, len(body), string(body))
		}
		sig, err := os.ReadFile(destSig)
		if err != nil {
			return fmt.Errorf("the signature did not travel with the bundle: %w (move reported:\n%s)", err, w.env.LastOutput())
		}
		if len(strings.TrimSpace(string(sig))) == 0 {
			return fmt.Errorf("%s is EMPTY — the bundle landed with a signature file carrying nothing", destSig)
		}
		if err := signing.Verify(body, sig, j001600Of(w).signer.Public, signing.NamespacePublish); err != nil {
			return fmt.Errorf("the relocated signature no longer verifies against the relocated bytes: %w", err)
		}
		return nil
	})

	ctx.Step(`^the source bundle "([^"]*)" and its signature are gone$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		for _, rel := range []string{j001600BundlePath(name), j001600BundlePath(name) + ".sig"} {
			if w.env.FileExists(rel) {
				return fmt.Errorf("%s still exists after the move", rel)
			}
		}
		return nil
	})

	ctx.Step(`^the source bundle "([^"]*)" and its signature are untouched$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		for _, rel := range []string{j001600BundlePath(name), j001600BundlePath(name) + ".sig"} {
			if !w.env.FileExists(rel) {
				return fmt.Errorf("%s was removed by a move that failed — a failed move must never eat the source (move reported:\n%s)", rel, w.env.LastOutput())
			}
		}
		return nil
	})

	ctx.Step(`^the shared standards directory is still empty$`, func(c context.Context) error {
		w := worldFrom(c)
		entries, err := os.ReadDir(j001600Of(w).sharedDir)
		if err != nil {
			return fmt.Errorf("read shared standards dir: %w", err)
		}
		if len(entries) != 0 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return fmt.Errorf("a refused move left %v at the destination", names)
		}
		return nil
	})
}

// j001600MarkerFor maps a fragment's human name to the marker string its content
// carries. Fails loud on an unknown name rather than silently asserting
// against "" — an empty needle is contained in every string, so a typo would
// turn every delivery assertion into a tautology.
func j001600MarkerFor(which string) (string, error) {
	switch which {
	case "tdd":
		return j001600TDDMarker, nil
	case "revised tdd":
		return j001600TDDRevised, nil
	case "curl-pipe-sh":
		return j001600CurlMarker, nil
	default:
		return "", fmt.Errorf("no J001600 marker is defined for %q", which)
	}
}
