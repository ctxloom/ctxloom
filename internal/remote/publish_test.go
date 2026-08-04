package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPublisher is a test double for Publisher.
type mockPublisher struct {
	files           map[string]string // path -> sha
	createdFiles    map[string][]byte
	branches        []string
	pullRequests    []mockPR
	createFileErr   error
	createBranchErr error
	createPRErr     error
	getFileSHAErr   error
}

type mockPR struct {
	title string
	body  string
	head  string
	base  string
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{
		files:        make(map[string]string),
		createdFiles: make(map[string][]byte),
		branches:     make([]string, 0),
		pullRequests: make([]mockPR, 0),
	}
}

func (m *mockPublisher) CreateOrUpdateFile(ctx context.Context, owner, repo, path, branch, message string, content []byte) (string, error) {
	if m.createFileErr != nil {
		return "", m.createFileErr
	}
	m.createdFiles[path] = content
	sha := "newsha123"
	m.files[path] = sha
	return sha, nil
}

func (m *mockPublisher) CreatePullRequest(ctx context.Context, owner, repo, title, body, head, base string) (string, error) {
	if m.createPRErr != nil {
		return "", m.createPRErr
	}
	m.pullRequests = append(m.pullRequests, mockPR{title: title, body: body, head: head, base: base})
	return "https://github.com/owner/repo/pull/1", nil
}

func (m *mockPublisher) CreateBranch(ctx context.Context, owner, repo, branchName, baseSHA string) error {
	if m.createBranchErr != nil {
		return m.createBranchErr
	}
	m.branches = append(m.branches, branchName)
	return nil
}

func (m *mockPublisher) GetFileSHA(ctx context.Context, owner, repo, path, ref string) (string, error) {
	if m.getFileSHAErr != nil {
		return "", m.getFileSHAErr
	}
	if sha, ok := m.files[path]; ok {
		return sha, nil
	}
	return "", nil
}

// mockPublisherFactory creates a PublisherFactory that returns the given publisher.
func mockPublisherFactory(p Publisher) PublisherFactory {
	return func(repoURL string, auth AuthConfig) (Publisher, error) {
		return p, nil
	}
}

func TestNewPublishManager(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))

	t.Run("creates with defaults", func(t *testing.T) {
		pm := NewPublishManager(registry, AuthConfig{})
		assert.NotNil(t, pm)
		assert.NotNil(t, pm.fs)
		assert.NotNil(t, pm.publisherFactory)
		assert.NotNil(t, pm.fetcherFactory)
	})

	t.Run("accepts custom options", func(t *testing.T) {
		customFS := afero.NewMemMapFs()
		mp := newMockPublisher()
		mf := newMockFetcher()

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(customFS),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		assert.Equal(t, customFS, pm.fs)
	})
}

// mybundleRemotePath is where every "/local/mybundle.yaml" fixture below
// publishes to. Publish no longer derives the remote path from the local
// filename — the caller computes it once, with PublishPath, and hands the same
// string to publish and to whatever reports the destination.
const mybundleRemotePath = ".ctxloom/content/bundles/mybundle.yaml"

func TestPublishManager_Publish(t *testing.T) {
	t.Run("publishes bundle successfully", func(t *testing.T) {
		fs := afero.NewMemMapFs()

		// Create local bundle file
		bundleContent := "description: Test bundle\nfragments:\n  test:\n    content: hello\n"
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte(bundleContent), 0644))

		// Create registry with remote
		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		// Create mock publisher and fetcher
		mp := newMockPublisher()
		mf := newMockFetcher()
		mf.defaultBranch = "main"

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(fs),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		result, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
			ItemType:   ItemTypeBundle,
			RemotePath: mybundleRemotePath,
			Branch:     "main",
		})

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, ".ctxloom/content/bundles/mybundle.yaml", result.Path)
		assert.Equal(t, "newsha123", result.SHA)
		assert.True(t, result.Created)

		// Verify file was created
		assert.Contains(t, mp.createdFiles, ".ctxloom/content/bundles/mybundle.yaml")
	})

	t.Run("creates PR when requested", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: Test\n"), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		mp := newMockPublisher()
		mf := newMockFetcher()
		mf.refs["main"] = "basesha123"

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(fs),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		result, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
			ItemType:   ItemTypeBundle,
			RemotePath: mybundleRemotePath,
			Branch:     "main",
			CreatePR:   true,
		})

		require.NoError(t, err)
		assert.NotEmpty(t, result.PRURL)
		assert.Len(t, mp.branches, 1)
		assert.Len(t, mp.pullRequests, 1)
	})

	t.Run("returns error for missing remote", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("test\n"), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))
		pm := NewPublishManager(registry, AuthConfig{}, WithPublishFS(fs))

		_, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "nonexistent", PublishOptions{
			ItemType:   ItemTypeBundle,
			RemotePath: mybundleRemotePath,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "remote not found")
	})

	t.Run("returns error for missing local file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		pm := NewPublishManager(registry, AuthConfig{}, WithPublishFS(fs))

		_, err := pm.Publish(context.Background(), "/nonexistent.yaml", "alice", PublishOptions{
			ItemType:   ItemTypeBundle,
			RemotePath: mybundleRemotePath,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read local file")
	})

	// loadPublishContent had no emptiness guard, so a 0-byte local
	// file published straight through, overwriting whatever real content
	// already existed at the remote path with nothing — success reported,
	// SHA returned, content silently destroyed. (The SIGNED half of this —
	// "a valid publisher signature over zero bytes" — is already
	// closed independently by signing.Sign's own floor; this pins
	// the UNSIGNED publish path, which Sign's floor cannot reach at all.)
	t.Run("refuses to publish an empty local file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte(""), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		mp := newMockPublisher()
		mf := newMockFetcher()

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(fs),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		_, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
			ItemType:   ItemTypeBundle,
			RemotePath: mybundleRemotePath,
			Branch:     "main",
		})

		require.Error(t, err, "publishing a 0-byte file must be refused, not overwrite the remote with nothing")
		assert.Empty(t, mp.createdFiles, "nothing must reach the remote")
	})

	// preparePublish used to swallow the error from GetFileSHA, silently
	// reinterpreting "the forge failed to answer" as "the file doesn't exist"
	// — flipping a genuine update into an "Add …" commit subject/PR title.
	t.Run("a GetFileSHA failure aborts the publish instead of being read as absent", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: Test\n"), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		mp := newMockPublisher()
		mp.getFileSHAErr = fmt.Errorf("forge unavailable")
		mf := newMockFetcher()

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(fs),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		_, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
			ItemType:   ItemTypeBundle,
			RemotePath: mybundleRemotePath,
			Branch:     "main",
		})

		require.Error(t, err, "a GetFileSHA failure must not be silently treated as \"file doesn't exist\"")
		assert.Empty(t, mp.createdFiles, "nothing must reach the remote when the existing-file check fails")
	})

	t.Run("detects update vs create", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: Test\n"), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		mp := newMockPublisher()
		mp.files[".ctxloom/content/bundles/mybundle.yaml"] = "existingsha" // File already exists
		mf := newMockFetcher()

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(fs),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		result, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
			ItemType:   ItemTypeBundle,
			RemotePath: mybundleRemotePath,
			Branch:     "main",
		})

		require.NoError(t, err)
		assert.False(t, result.Created)
	})

	// RemotePath is REQUIRED. Publish used to derive it from the local
	// filename, so there was no way to omit it; now that the caller supplies
	// it, a caller who forgets must be told rather than have a destination
	// guessed for them — a zero value here would address the repo root, and the
	// publish would report success for a file nobody meant to write.
	t.Run("refuses to publish with no remote path", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: Test\n"), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		mp := newMockPublisher()
		mf := newMockFetcher()
		mf.defaultBranch = "main"

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(fs),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		_, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
			ItemType: ItemTypeBundle,
			Branch:   "main",
		})

		require.Error(t, err, "an unset RemotePath must be refused, not guessed")
		assert.Contains(t, err.Error(), "RemotePath")
		assert.Empty(t, mp.createdFiles, "nothing must reach the remote")
	})
}

func TestPublishPath(t *testing.T) {
	tests := []struct {
		itemType ItemType
		name     string
		expected string
	}{
		{ItemTypeBundle, "security", ".ctxloom/content/bundles/security.yaml"},
		{ItemTypeBundle, "testing", ".ctxloom/content/bundles/testing.yaml"},
		{ItemType(""), "unknown", ".ctxloom/content/bundles/unknown.yaml"}, // defaults to bundles
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := PublishPath(tt.itemType, tt.name)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// PUBLISH AND FETCH MUST NAME THE SAME FILE. PublishPath decides where a
// bundle lands; Reference.BuildFilePath decides where a consumer looks for it
// by name. Nothing structural ties the two expressions together, and they were
// out of step for directory-form bundles for as long as publishing named them
// after their `bundle.yaml` manifest: they published as "bundle" and so were
// reachable only under that name, if at all.
//
// This asserts the agreement for the MANIFEST, which is all that publishing
// writes. A directory-form bundle's skills/ subtree is still neither published
// nor resolvable by ref — that gap is real and remains open.
func TestPublishPath_MatchesFetchSideRefResolution(t *testing.T) {
	for _, name := range []string{"security", "dir-form", "lang/go/testing"} {
		t.Run(name, func(t *testing.T) {
			ref := &Reference{Path: name, ItemType: ItemTypeBundle}
			assert.Equal(t, ref.BuildFilePath(ItemTypeBundle), PublishPath(ItemTypeBundle, name),
				"a bundle published under a name must be the file a ref to that name resolves to")
		})
	}
}

// --- Exact-bytes publishing (signature-envelope spec §3.0, §3.1) -----------

// publishOnce runs a single publish of content through a fresh mock publisher
// and returns the bytes that landed at the remote bundle path.
func publishOnce(t *testing.T, content string, opts ...func(*PublishOptions)) []byte {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/local", 0755))
	require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte(content), 0644))

	registry, _ := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	mp := newMockPublisher()
	mf := newMockFetcher()
	mf.defaultBranch = "main"

	pm := NewPublishManager(registry, AuthConfig{},
		WithPublishFS(fs),
		WithPublisherFactory(mockPublisherFactory(mp)),
		WithPublishFetcherFactory(mockFetcherFactory(mf)),
	)

	po := PublishOptions{ItemType: ItemTypeBundle, RemotePath: mybundleRemotePath, Branch: "main"}
	for _, o := range opts {
		o(&po)
	}
	_, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", po)
	require.NoError(t, err)
	return mp.createdFiles[".ctxloom/content/bundles/mybundle.yaml"]
}

func TestPublishManager_Publish_WritesLocalBytesVerbatim(t *testing.T) {
	// Comments, key order, blank lines, trailing whitespace: every byte of the
	// author's file must reach the remote untouched. Nothing prepended,
	// appended, or normalized (spec §3.0, §3.1).
	content := "# leading comment\nzebra: 1 # inline\n\nalpha: 2\n"

	published := publishOnce(t, content)

	assert.Equal(t, content, string(published),
		"publish must write the local file's bytes verbatim — no metadata injection, no re-serialization")
}

func TestPublishManager_Publish_IsReproducible(t *testing.T) {
	// Publishing the same unchanged bundle twice must produce IDENTICAL bytes.
	// A wall-clock stamp in the body (the old `_published` block) broke this,
	// and with it any consumer's ability to check the remote against the
	// author's file.
	content := "description: Test bundle\n"

	first := publishOnce(t, content)
	// Cross a wall-clock second boundary between the two publishes. The old
	// stamp was RFC3339 (second granularity), so back-to-back publishes inside
	// one second produced identical bytes by luck — which is how a
	// non-reproducible publish went unnoticed. Straddling the boundary makes
	// any clock-derived content fail deterministically rather than flakily.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(1100 * time.Millisecond)))
	second := publishOnce(t, content)

	assert.Equal(t, string(first), string(second),
		"two publishes of an unchanged bundle must produce byte-identical remote content")
}

func TestPublishManager_Publish_LocalFileSignatureVerifiesAgainstPublishedBytes(t *testing.T) {
	// The invariant this whole change exists to restore: a signature produced
	// over the LOCAL file's bytes (what `ctxloom sign` does — operations.
	// SignBundleFile signs the file on disk verbatim) must verify against the
	// bytes that land in the remote. It only can if they are the same bytes.
	content := "description: Test bundle\nfragments:\n  test:\n    content: hello\n"

	// Stand-in for a real sshsig: a signature is any deterministic function of
	// the payload, and verification is recomputing it over the candidate bytes.
	sign := func(payload []byte) []byte {
		sum := sha256.Sum256(payload)
		return []byte("sig:" + hex.EncodeToString(sum[:]))
	}
	// Detached signature computed offline against the local file, before push.
	localSig := sign([]byte(content))

	var signedPayload []byte
	published := publishOnce(t, content, func(po *PublishOptions) {
		po.SignPayload = func(payload []byte) ([]byte, error) {
			signedPayload = payload
			return sign(payload), nil
		}
	})

	assert.Equal(t, string(localSig), string(sign(published)),
		"a signature over the local file must verify against the published bytes")
	assert.Equal(t, string(content), string(signedPayload),
		"SignPayload must be handed the local file's exact bytes")
}

func TestNewPublisher(t *testing.T) {
	t.Run("creates GitHub publisher for GitHub URL", func(t *testing.T) {
		publisher, err := NewPublisher("https://github.com/owner/repo", AuthConfig{GitHub: "token"})
		require.NoError(t, err)
		assert.NotNil(t, publisher)
		// The publisher should be a GitHubPublisher (though we can't check the exact type)
	})

	// This used to assert a REFUSAL ("publishing to generic git hosts is not
	// supported"), which is why publish only ever ran against GitHub and why
	// whole-tree publish could only be verified against a mock Publisher.
	t.Run("builds a git publisher for a generic git host", func(t *testing.T) {
		publisher, err := NewPublisher("https://gitlab.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.IsType(t, &GitPublisher{}, publisher,
			"a non-GitHub host must publish through plain git, not the forge API")
	})

	t.Run("builds a git publisher for a file:// remote", func(t *testing.T) {
		publisher, err := NewPublisher("file:///srv/bundles.git", AuthConfig{})
		require.NoError(t, err)
		assert.IsType(t, &GitPublisher{}, publisher)
	})

	t.Run("builds a git publisher for an scp-style ssh remote", func(t *testing.T) {
		publisher, err := NewPublisher("git@git.example.com:team/bundles.git", AuthConfig{})
		require.NoError(t, err)
		gp, ok := publisher.(*GitPublisher)
		require.True(t, ok)
		assert.Equal(t, "git@git.example.com:team/bundles.git", gp.repoURL,
			"the scp transport must survive: rewriting it to https would discard the user's ssh key with it")
	})

	t.Run("creates GitHub publisher for shorthand", func(t *testing.T) {
		publisher, err := NewPublisher("owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.NotNil(t, publisher)
	})
}

func TestDefaultPublisherFactory(t *testing.T) {
	t.Run("creates GitHub publisher", func(t *testing.T) {
		publisher, err := defaultPublisherFactory("https://github.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.NotNil(t, publisher)
	})

	t.Run("creates a git publisher for a generic git host", func(t *testing.T) {
		publisher, err := defaultPublisherFactory("https://gitlab.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.IsType(t, &GitPublisher{}, publisher)
	})
}

func TestSplitTitleBody(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		body      string
		wantTitle string
		wantBody  string
	}{
		{"explicit title kept, body trimmed", "My Title", "  some body  ", "My Title", "some body"},
		{"empty both", "", "", "", ""},
		{"lift first line into title", "", "Subject line\nbody para", "Subject line", "body para"},
		{"single-line body becomes title, body emptied", "", "Just a subject", "Just a subject", ""},
		{"title set, body has newlines untouched", "T", "a\nb", "T", "a\nb"},
		{"whitespace-only inputs", "   ", "   ", "", ""},
		{"lift trims around the split", "", "  Subject  \n  rest  ", "Subject", "rest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTitle, gotBody := SplitTitleBody(tt.title, tt.body)
			assert.Equal(t, tt.wantTitle, gotTitle, "title")
			assert.Equal(t, tt.wantBody, gotBody, "body")
		})
	}
}

// --- SignPayload wiring (signature-envelope spec §7A) ----------------------

func TestPublishManager_Publish_SignPayloadWritesSiblingSig(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/local", 0755))
	require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: Test\n"), 0644))

	registry, _ := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	mp := newMockPublisher()
	mf := newMockFetcher()
	mf.defaultBranch = "main"

	pm := NewPublishManager(registry, AuthConfig{},
		WithPublishFS(fs),
		WithPublisherFactory(mockPublisherFactory(mp)),
		WithPublishFetcherFactory(mockFetcherFactory(mf)),
	)

	var signedPayload []byte
	result, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
		Branch:     "main",
		SignPayload: func(payload []byte) ([]byte, error) {
			signedPayload = payload
			return []byte("FAKE-SIGNATURE"), nil
		},
	})

	require.NoError(t, err)
	assert.True(t, result.Signed)
	require.Contains(t, mp.createdFiles, ".ctxloom/content/bundles/mybundle.yaml.sig")
	assert.Equal(t, []byte("FAKE-SIGNATURE"), mp.createdFiles[".ctxloom/content/bundles/mybundle.yaml.sig"])
	// The signed payload must be EXACTLY the bytes that landed at the main
	// path, and those must be the local file's bytes — no injection, no
	// re-serialization anywhere in the path (spec §3.0, §3.1).
	assert.Equal(t, mp.createdFiles[".ctxloom/content/bundles/mybundle.yaml"], signedPayload)
	assert.Equal(t, "description: Test\n", string(signedPayload))
}

func TestPublishManager_Publish_SignPayloadFailureAbortsBeforeAnyWrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/local", 0755))
	require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: Test\n"), 0644))

	registry, _ := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	mp := newMockPublisher()
	mf := newMockFetcher()
	mf.defaultBranch = "main"

	pm := NewPublishManager(registry, AuthConfig{},
		WithPublishFS(fs),
		WithPublisherFactory(mockPublisherFactory(mp)),
		WithPublishFetcherFactory(mockFetcherFactory(mf)),
	)

	_, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
		Branch:     "main",
		SignPayload: func(payload []byte) ([]byte, error) {
			return nil, fmt.Errorf("no signing key found")
		},
	})

	require.Error(t, err, "a signing failure must abort the publish, never degrade to unsigned")
	assert.Empty(t, mp.createdFiles, "no file — signed or unsigned — may be written when signing was requested and failed")
}

func TestPublishManager_Publish_NoSignPayloadMeansNoSigWritten(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/local", 0755))
	require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: Test\n"), 0644))

	registry, _ := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	mp := newMockPublisher()
	mf := newMockFetcher()
	mf.defaultBranch = "main"

	pm := NewPublishManager(registry, AuthConfig{},
		WithPublishFS(fs),
		WithPublisherFactory(mockPublisherFactory(mp)),
		WithPublishFetcherFactory(mockFetcherFactory(mf)),
	)

	result, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
		Branch:     "main",
	})
	require.NoError(t, err)
	assert.False(t, result.Signed)
	assert.NotContains(t, mp.createdFiles, ".ctxloom/content/bundles/mybundle.yaml.sig")
}
