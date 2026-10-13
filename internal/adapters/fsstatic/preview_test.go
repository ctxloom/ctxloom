package fsstatic_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The preview (delivery.Static.Preview, wieldable-squander, decision #3):
// what a Deliver of the same loadout would change, per kind, writing
// nothing.

// differing is the kinds of p that differ.
func differing(p delivery.Previewed) []present.Kind {
	var out []present.Kind
	for _, k := range p.Kinds {
		if k.Differs() {
			out = append(out, k.Kind)
		}
	}
	return out
}

// kindOf is p's preview of kind k.
func kindOf(t *testing.T, p delivery.Previewed, k present.Kind) delivery.KindPreview {
	t.Helper()
	for _, kp := range p.Kinds {
		if kp.Kind == k {
			return kp
		}
	}
	t.Fatalf("the preview has no entry for %v", k)
	return delivery.KindPreview{}
}

// mockAt is the mock's every-kind fixture over project with the per-kind
// project target and a static writer over fs.
type mockAt struct {
	static  *fsstatic.Static
	target  delivery.Target
	root    engine.Base
	project string
}

func newMockAt(t *testing.T, fs afero.Fs, project string) mockAt {
	t.Helper()
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	root := mock.New().Root()
	return mockAt{
		static:  fsstatic.New(safefs.NewMem(fs)),
		target:  delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriterFor(root.Name), Kinds: delivery.AllKinds()},
		root:    root,
		project: project,
	}
}

func (m mockAt) loadout(t *testing.T, body string) delivery.Loadout {
	t.Helper()
	pkg, plan, exports, _ := kindsFixture(t, m.project, body)
	return delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}
}

func (m mockAt) deliver(t *testing.T, lo delivery.Loadout) {
	t.Helper()
	_, err := m.static.Deliver(context.Background(), lo, m.root, m.target)
	require.NoError(t, err)
}

func (m mockAt) preview(t *testing.T, lo delivery.Loadout) delivery.Previewed {
	t.Helper()
	p, err := m.static.Preview(context.Background(), lo, m.root, m.target)
	require.NoError(t, err)
	return p
}

// Right after a delivery, previewing the same loadout finds nothing: every
// kind the target speaks for is listed, none differs and none is refused.
// MUTATION: report every planned kind as changed → red.
func TestPreview_RightAfterADeliveryNothingDiffers(t *testing.T) {
	m := newMockAt(t, afero.NewOsFs(), t.TempDir())
	lo := m.loadout(t, "hello")
	m.deliver(t, lo)

	p := m.preview(t, lo)

	require.Len(t, p.Kinds, len(delivery.AllKinds()))
	assert.Empty(t, differing(p))
	for _, k := range p.Kinds {
		assert.NoError(t, k.Refused, "%v", k.Kind)
	}
}

// A change to one kind's input differs in exactly that kind, naming the file
// that would change.
func TestPreview_AChangeToOneKindNamesExactlyThatKind(t *testing.T) {
	m := newMockAt(t, afero.NewOsFs(), t.TempDir())
	m.deliver(t, m.loadout(t, "first"))

	p := m.preview(t, m.loadout(t, "second"))

	require.Equal(t, []present.Kind{present.Context}, differing(p))
	assert.Equal(t, []string{filepath.Join(m.project, mock.ContextFileName)}, kindOf(t, p, present.Context).Changed)
}

// A kind the target speaks for that the plan no longer routes releases the
// claims it holds: it differs, naming each file it would release in.
func TestPreview_AKindThePlanDropsIsReleased(t *testing.T) {
	m := newMockAt(t, afero.NewOsFs(), t.TempDir())
	lo := m.loadout(t, "hello")
	m.deliver(t, lo)
	lo.Plan = only(lo.Plan, present.Context, present.MCP, present.Settings, present.Hooks, present.Skills)

	p := m.preview(t, lo)

	require.Equal(t, []present.Kind{present.Commands}, differing(p))
	cmd := filepath.Join(m.project, ".mock", "commands", "go.md")
	assert.Equal(t, []string{cmd}, kindOf(t, p, present.Commands).Released)
	assert.Equal(t, []string{cmd}, kindOf(t, p, present.Commands).Changed, "the file it alone claimed goes")
}

// A hand edit a delivery would overwrite differs: a delivered file the user
// deleted is written back.
func TestPreview_AHandEditTheDeliveryWouldUndoDiffers(t *testing.T) {
	fs := afero.NewOsFs()
	m := newMockAt(t, fs, t.TempDir())
	lo := m.loadout(t, "hello")
	m.deliver(t, lo)
	require.NoError(t, fs.Remove(filepath.Join(m.project, ".mock", "commands", "go.md")))

	p := m.preview(t, lo)

	require.Equal(t, []present.Kind{present.Commands}, differing(p))
}

// A refusal the delivery would meet is the kind's, and the other kinds are
// still previewed: here the user wrote after ctxloom's context section, so
// the section a new context would replace is no longer ctxloom's to cut out.
func TestPreview_ARefusalIsTheKindsAndTheRestArePreviewed(t *testing.T) {
	fs := afero.NewOsFs()
	m := newMockAt(t, fs, t.TempDir())
	m.deliver(t, m.loadout(t, "first"))
	path := filepath.Join(m.project, mock.ContextFileName)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(body, []byte("\nmy notes, after it\n")...), 0o644))
	lo := m.loadout(t, "second")
	lo.Plan = only(lo.Plan, present.Context, present.MCP, present.Settings, present.Hooks, present.Skills)

	p := m.preview(t, lo)

	ctx := kindOf(t, p, present.Context)
	require.ErrorIs(t, ctx.Refused, fsstatic.ErrNotOurs)
	assert.False(t, ctx.Differs(), "a refused kind lands nothing")
	assert.Equal(t, []present.Kind{present.Commands}, differing(p), "the other kinds were still previewed")
}

// A declared file that no longer stands is the kind's refusal
// (ErrDeclaredMissing), as it is a delivery's.
func TestPreview_ADeclaredFileMissingIsTheKindsRefusal(t *testing.T) {
	f := newDeclaredFixture(t)
	root := mock.New().Root()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"))
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{present.Context: present.RootProjectRoot}}
	plan, err := delivery.Route(pkg.EngineItems(root.Name), root, pref, present.Paths{ProjectRoot: present.Root{Host: f.project, Engine: f.project}})
	require.NoError(t, err)
	target := delivery.Target{Root: present.ProjectOnHost(f.project), Ownership: f.rec, Writer: delivery.ProjectWriterFor("mock"), Kinds: []present.Kind{present.Context}}
	static := fsstatic.New(safefs.NewMem(f.fs))
	lo := delivery.Loadout{Plan: plan, Package: pkg}
	root.Context = contextBy{ContextApproach: root.Context, deliver: writeAndDeclare(f.path)}
	_, err = static.Deliver(context.Background(), lo, root, target)
	require.NoError(t, err)
	require.NoError(t, f.fs.Remove(f.path))
	root.Context = contextBy{ContextApproach: root.Context, deliver: declareOnly(f.path)}

	p, err := static.Preview(context.Background(), lo, root, target)

	require.NoError(t, err)
	require.ErrorIs(t, kindOf(t, p, present.Context).Refused, fsstatic.ErrDeclaredMissing)
}

// A target with one writer for every kind (a session's) cannot be previewed
// per kind.
func TestPreview_RefusesATargetThatDoesNotSpeakPerKind(t *testing.T) {
	m := newMockAt(t, afero.NewMemMapFs(), t.TempDir())
	m.target.Kinds = nil
	_, err := m.static.Preview(context.Background(), m.loadout(t, "hello"), m.root, m.target)
	require.ErrorIs(t, err, delivery.ErrPreviewNeedsKinds)
}

// claudeShared is Claude's settings and hooks — two kinds claiming into one
// .claude/settings.json — delivered under per-kind writers.
type claudeShared struct {
	static  *fsstatic.Static
	target  delivery.Target
	root    engine.Base
	project string
}

func newClaudeShared(t *testing.T, fs afero.Fs) claudeShared {
	t.Helper()
	eng, err := claude.Build()
	require.NoError(t, err)
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	project := t.TempDir()
	return claudeShared{
		static:  fsstatic.New(safefs.NewMem(fs)),
		target:  delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriterFor(eng.Root().Name), Kinds: []present.Kind{present.Settings, present.Hooks}},
		root:    eng.Root(),
		project: project,
	}
}

func (c claudeShared) loadout(t *testing.T, deny []string, hooks ...wire.Hook) delivery.Loadout {
	t.Helper()
	pkg := compositetest.Fixture(t)
	pkg.DenyTools = deny
	pkg.Hooks.Unified.PreTool = hooks
	eng, err := claude.Build()
	require.NoError(t, err)
	items := pkg.EngineItems(c.root.Name)
	exports, err := eng.Exports(items)
	require.NoError(t, err)
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{present.Settings: present.RootProjectRoot, present.Hooks: present.RootProjectRoot}}
	plan, err := delivery.Route(items, c.root, pref, present.Paths{ProjectRoot: present.Root{Host: c.project, Engine: c.project}})
	require.NoError(t, err)
	return delivery.Loadout{Plan: only(plan, present.Settings, present.Hooks), Package: pkg, Exports: exports}
}

// In a file two kinds share, a change one kind makes is that kind's alone:
// each kind is folded under its own writer against the file as it stands.
// MUTATION: fold every kind's ops in one batch and attribute each changed
// file to every kind that claims in it → red.
func TestPreview_InASharedFileAChangeIsAttributedToTheKindThatMadeIt(t *testing.T) {
	c := newClaudeShared(t, afero.NewOsFs())
	first := c.loadout(t, []string{"Bash"}, wire.Hook{Type: "command", Command: "true"})
	_, err := c.static.Deliver(context.Background(), first, c.root, c.target)
	require.NoError(t, err)
	settings := filepath.Join(c.project, ".claude", "settings.json")

	same, err := c.static.Preview(context.Background(), first, c.root, c.target)
	require.NoError(t, err)
	assert.Empty(t, differing(same))

	hooksChanged, err := c.static.Preview(context.Background(), c.loadout(t, []string{"Bash"}, wire.Hook{Type: "command", Command: "false"}), c.root, c.target)
	require.NoError(t, err)
	assert.Equal(t, []present.Kind{present.Hooks}, differing(hooksChanged))
	assert.Equal(t, []string{settings}, kindOf(t, hooksChanged, present.Hooks).Changed)

	settingsChanged, err := c.static.Preview(context.Background(), c.loadout(t, []string{"Bash", "Write"}, wire.Hook{Type: "command", Command: "true"}), c.root, c.target)
	require.NoError(t, err)
	assert.Equal(t, []present.Kind{present.Settings}, differing(settingsChanged))
}

// A place another kind claims that the user took out of the shared file is
// put back in THAT kind's name only: the settings kind, folded alone, moves
// only the places it claims, so the hooks the user deleted are the hooks
// kind's difference and not the settings kind's.
// MUTATION: fold each kind unscoped (drop Staging.scopeTo) → red.
func TestPreview_ALostPlaceIsTheKindThatClaimsIt(t *testing.T) {
	fs := afero.NewOsFs()
	c := newClaudeShared(t, fs)
	lo := c.loadout(t, []string{"Bash"}, wire.Hook{Type: "command", Command: "true"})
	_, err := c.static.Deliver(context.Background(), lo, c.root, c.target)
	require.NoError(t, err)
	settings := filepath.Join(c.project, ".claude", "settings.json")
	raw, err := os.ReadFile(settings)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Contains(t, doc, "hooks")
	delete(doc, "hooks")
	edited, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, safefs.WriteFile(fs, settings, edited, 0o644))

	p, err := c.static.Preview(context.Background(), lo, c.root, c.target)

	require.NoError(t, err)
	assert.Equal(t, []present.Kind{present.Hooks}, differing(p))
}

// mutationFs records every operation that could change the filesystem and
// refuses it; reads go to base.
type mutationFs struct {
	afero.Fs
	mu  sync.Mutex
	ops []string
}

func newMutationFs(base afero.Fs) *mutationFs { return &mutationFs{Fs: base} }

// refuse records op on name and refuses it.
func (m *mutationFs) refuse(op, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ops = append(m.ops, op+" "+name)
	return &os.PathError{Op: op, Path: name, Err: os.ErrPermission}
}

func (m *mutationFs) Create(name string) (afero.File, error) { return nil, m.refuse("create", name) }
func (m *mutationFs) Mkdir(name string, _ os.FileMode) error { return m.refuse("mkdir", name) }
func (m *mutationFs) MkdirAll(name string, _ os.FileMode) error {
	return m.refuse("mkdirall", name)
}
func (m *mutationFs) Remove(name string) error    { return m.refuse("remove", name) }
func (m *mutationFs) RemoveAll(name string) error { return m.refuse("removeall", name) }
func (m *mutationFs) Rename(_, n string) error    { return m.refuse("rename", n) }
func (m *mutationFs) Chmod(name string, _ os.FileMode) error {
	return m.refuse("chmod", name)
}
func (m *mutationFs) Chtimes(name string, _, _ time.Time) error { return m.refuse("chtimes", name) }
func (m *mutationFs) Chown(name string, _, _ int) error         { return m.refuse("chown", name) }

func (m *mutationFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 {
		return nil, m.refuse("open-write", name)
	}
	return m.Fs.OpenFile(name, flag, perm)
}

// The preview writes nothing — no target file, no record, no approach's own
// state, no directory — even where the delivery it previews would write all
// of them: a project that does not exist yet, every kind new, and an
// approach that keeps state outside the target. Both the static writer and
// the record go through a filesystem that records every change and makes
// none.
// MUTATION: Commit the preview's batch, or write the own state → red.
func TestPreview_WritesNothing(t *testing.T) {
	base := afero.NewMemMapFs()
	fs := newMutationFs(base)
	project := filepath.Join(t.TempDir(), "not-yet")
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	own := filepath.Join(t.TempDir(), "state", "own.yaml")
	pkg, plan, exports, root := kindsFixture(t, project, "hello")
	inner := root.Context
	root.Context = contextBy{ContextApproach: inner, deliver: func(afs afero.Fs) (present.Delivered, error) {
		if err := afs.MkdirAll(filepath.Dir(own), 0o700); err != nil {
			return present.Delivered{}, err
		}
		if err := safefs.WriteFile(afs, own, []byte("state"), 0o600); err != nil {
			return present.Delivered{}, err
		}
		return inner.DeliverContext(present.ProjectOnHost(project), present.RootProjectRoot, engine.ContextInputs{Text: []byte("hello")}, afs)
	}}
	target := delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriterFor(root.Name), Kinds: delivery.AllKinds()}

	// The locks live apart from the files, as the home lock directory does
	// in production: taking one is not a write into the project or the record.
	files := safefs.NewMem(fs)
	files.Locks = safefs.NewMem(afero.NewMemMapFs()).Locks

	p, err := fsstatic.New(files).Preview(context.Background(), delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}, root, target)

	require.NoError(t, err)
	assert.ElementsMatch(t, delivery.AllKinds(), differing(p), "every kind is new")
	for _, k := range p.Kinds {
		assert.NoError(t, k.Refused, "%v", k.Kind)
	}
	assert.Empty(t, fs.ops, "the preview changed nothing")
	exists, err := afero.Exists(base, project)
	require.NoError(t, err)
	assert.False(t, exists)
}
