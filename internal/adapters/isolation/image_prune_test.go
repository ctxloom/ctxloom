package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storeImage is one image in the scripted store.
type storeImage struct {
	id      string
	refs    []string
	labels  map[string]string
	created time.Time
	unique  string // humanized, as `system df -v` renders it
}

// fakeImageStore scripts a docker-shaped runtime behind the probeExec seam:
// it answers the listing, inspect, ps and df argv PlanImagePrune issues from
// canned state and records every call (every rmi in particular), so the prune
// is exercised end to end through the real Runtime grammar with no daemon and
// no real image anywhere near it.
type fakeImageStore struct {
	images     []storeImage
	containers map[string]string // container ID -> image ID
	failRmi    map[string]bool   // ref -> the runtime refuses it
	calls      [][]string
}

func (s *fakeImageStore) install(t *testing.T) fakeRuntime {
	t.Helper()
	orig := probeExec
	probeExec = s.exec
	t.Cleanup(func() { probeExec = orig })
	return fakeRuntime{name: "docker", binary: "fake-docker", available: true}
}

func (s *fakeImageStore) rmis() [][]string {
	var out [][]string
	for _, c := range s.calls {
		if c[0] == "rmi" {
			out = append(out, c)
		}
	}
	return out
}

func (s *fakeImageStore) exec(_ context.Context, _ string, args []string) (string, error) {
	s.calls = append(s.calls, append([]string(nil), args...))
	switch {
	case args[0] == "images":
		return s.list(args[len(args)-1]), nil
	case args[0] == "image" && args[1] == "inspect":
		return s.inspect(args[2:]), nil
	case args[0] == "ps":
		var ids []string
		for c := range s.containers {
			ids = append(ids, c)
		}
		return strings.Join(ids, "\n"), nil
	case args[0] == "container" && args[1] == "inspect":
		var ids []string
		for _, c := range args[4:] {
			ids = append(ids, "sha256:"+s.containers[c])
		}
		return strings.Join(ids, "\n"), nil
	case args[0] == "system":
		return s.df(), nil
	case args[0] == "rmi":
		for _, ref := range args[1:] {
			if s.failRmi[ref] {
				return "", errors.New("exit status 1")
			}
		}
		return "", nil
	}
	return "", fmt.Errorf("fake store: unscripted argv %v", args)
}

func (s *fakeImageStore) list(filter string) string {
	kind, val, _ := strings.Cut(filter, "=")
	var ids []string
	for _, img := range s.images {
		if s.matches(img, kind, val) {
			ids = append(ids, "sha256:"+img.id)
		}
	}
	return strings.Join(ids, "\n")
}

func (s *fakeImageStore) matches(img storeImage, kind, val string) bool {
	if kind == "label" {
		_, ok := img.labels[val]
		return ok
	}
	for _, ref := range img.refs {
		repo, _, _ := strings.Cut(ref, ":")
		if ok, _ := path.Match(val, repo); ok {
			return true
		}
	}
	return false
}

func (s *fakeImageStore) inspect(ids []string) string {
	var out []map[string]any
	for _, id := range ids {
		for _, img := range s.images {
			if img.id == normalizeImageID(id) {
				out = append(out, map[string]any{
					"Id": "sha256:" + img.id, "RepoTags": img.refs, "Created": img.created,
					"Config": map[string]any{"Labels": img.labels},
				})
			}
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func (s *fakeImageStore) df() string {
	var imgs []map[string]string
	for _, img := range s.images {
		imgs = append(imgs, map[string]string{"ID": "sha256:" + img.id, "UniqueSize": img.unique})
	}
	b, _ := json.Marshal(map[string]any{"Images": imgs})
	return string(b)
}

// Fixture builders. now is the reference instant every age is taken from.
var pruneNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

var fixtureSeq int

func fixtureID() string {
	fixtureSeq++
	return fmt.Sprintf("%012x%052x", fixtureSeq, fixtureSeq) // distinct in the 12-hex short ID df keys by
}

// composedImg is a labelled composed image of engine mock, age old.
func composedImg(ref, slot, companions string, age time.Duration, from string) storeImage {
	labels := map[string]string{
		labelImageKind: string(ImageComposed), labelImageSlot: slot, labelImageCompanions: companions,
		labelEngine: "mock", provenanceLabel: "p",
	}
	if from != "" {
		labels[labelImageFrom] = from
	}
	return storeImage{id: fixtureID(), refs: []string{ref}, labels: labels, created: pruneNow.Add(-age), unique: "40MB"}
}

func baseImg(ref, slot string, age time.Duration) storeImage {
	return storeImage{id: fixtureID(), refs: []string{ref}, created: pruneNow.Add(-age), unique: "300MB",
		labels: map[string]string{labelImageKind: string(ImageBase), labelImageSlot: slot}}
}

func unlabelledImg(ref string) storeImage {
	return storeImage{id: fixtureID(), refs: []string{ref}, created: pruneNow.Add(-90 * 24 * time.Hour), unique: "300MB"}
}

const day = 24 * time.Hour

// verdictsByRef flattens a plan for assertions.
func verdictsByRef(p ImagePrunePlan) map[string]KeepReason {
	out := map[string]KeepReason{}
	for _, v := range p.Verdicts {
		out[v.Image.name()] = v.Keep
	}
	return out
}

func planFor(t *testing.T, s *fakeImageStore, live ...string) (ImagePrunePlan, fakeRuntime) {
	t.Helper()
	rt := s.install(t)
	plan, err := PlanImagePrune(context.Background(), rt, ImagePruneOptions{Live: live, MinAge: day, Now: pruneNow})
	require.NoError(t, err)
	return plan, rt
}

// TestPlanImagePrune_KeepRules is the spec's table: each case is one keep
// rule (or its absence) decided over a scripted store.
func TestPlanImagePrune_KeepRules(t *testing.T) {
	cases := []struct {
		name       string
		images     []storeImage
		containers func([]storeImage) map[string]string
		live       []string
		want       map[string]KeepReason
		unowned    []string
	}{
		{
			name: "superseded same-slot tags go; the newest in the slot stays",
			images: []storeImage{
				composedImg("ctxloom-agent-mock:v1-cC-S", "S", "C", 5*day, ""),
				composedImg("ctxloom-agent-mock:v2-cC-S", "S", "C", 4*day, ""),
				composedImg("ctxloom-agent-mock:v3-cC-S", "S", "C", 3*day, ""),
			},
			want: map[string]KeepReason{
				"ctxloom-agent-mock:v1-cC-S": Superseded,
				"ctxloom-agent-mock:v2-cC-S": Superseded,
				"ctxloom-agent-mock:v3-cC-S": KeepNewestInSlot,
			},
		},
		{
			name: "the current identity is kept even when it is not the newest (a downgraded binary)",
			images: []storeImage{
				composedImg("ctxloom-agent-mock:v1-cC-S", "S", "C", 5*day, ""),
				composedImg("ctxloom-agent-mock:v2-cC-S", "S", "C", 3*day, ""),
			},
			live: []string{"ctxloom-agent-mock:v1-cC-S"},
			want: map[string]KeepReason{
				"ctxloom-agent-mock:v1-cC-S": KeepCurrent,
				"ctxloom-agent-mock:v2-cC-S": KeepNewestInSlot,
			},
		},
		{
			name: "an image a STOPPED container references is kept",
			images: []storeImage{
				composedImg("ctxloom-agent-mock:v1-cC-S", "S", "C", 5*day, ""),
				composedImg("ctxloom-agent-mock:v2-cC-S", "S", "C", 3*day, ""),
			},
			containers: func(imgs []storeImage) map[string]string { return map[string]string{"stopped1": imgs[0].id} },
			want: map[string]KeepReason{
				"ctxloom-agent-mock:v1-cC-S": KeepReferenced,
				"ctxloom-agent-mock:v2-cC-S": KeepNewestInSlot,
			},
		},
		{
			name: "an image younger than min-age is kept",
			images: []storeImage{
				composedImg("ctxloom-agent-mock:v1-cC-S", "S", "C", 2*time.Hour, ""),
				composedImg("ctxloom-agent-mock:v2-cC-S", "S", "C", time.Hour, ""),
			},
			want: map[string]KeepReason{
				"ctxloom-agent-mock:v1-cC-S": KeepYoung,
				"ctxloom-agent-mock:v2-cC-S": KeepNewestInSlot,
			},
		},
		{
			name: "slots differing only in companions are kept separately (developer HOME vs acceptance cell)",
			images: []storeImage{
				composedImg("ctxloom-agent-mock:v1-cDEV-S", "S", "DEV", 5*day, ""),
				composedImg("ctxloom-agent-mock:v1-cCELL-S", "S", "CELL", 3*day, ""),
			},
			want: map[string]KeepReason{
				"ctxloom-agent-mock:v1-cDEV-S":  KeepNewestInSlot,
				"ctxloom-agent-mock:v1-cCELL-S": KeepNewestInSlot,
			},
		},
		{
			name: "an unlabelled ctxloom-agent-base:latest and a user's ctxloom-agent-x are skipped, never owned",
			images: []storeImage{
				unlabelledImg("ctxloom-agent-base:latest"),
				unlabelledImg("ctxloom-agent-x:mine"),
			},
			want:    map[string]KeepReason{},
			unowned: []string{"ctxloom-agent-base:latest", "ctxloom-agent-x:mine"},
		},
		{
			name: "a base a kept image names via ctxloom.from is kept though not newest in its slot",
			images: []storeImage{
				baseImg("ctxloom-agent-base:old", "B", 9*day),
				baseImg("ctxloom-agent-base:new", "B", 8*day),
				composedImg("ctxloom-agent-mock:v1-cC-S", "S", "C", 5*day, "ctxloom-agent-base:old"),
			},
			want: map[string]KeepReason{
				"ctxloom-agent-base:old":     KeepParent,
				"ctxloom-agent-base:new":     KeepNewestInSlot,
				"ctxloom-agent-mock:v1-cC-S": KeepNewestInSlot,
			},
		},
		{
			name: "a pre-label image (provenance+engine, no slot) is owned but never newest-in-slot",
			images: []storeImage{{
				id: fixtureID(), refs: []string{"ctxloom-agent-claude-code:old"}, created: pruneNow.Add(-30 * day), unique: "700MB",
				labels: map[string]string{provenanceLabel: "p", labelEngine: "claude-code"},
			}},
			want: map[string]KeepReason{"ctxloom-agent-claude-code:old": Superseded},
		},
		{
			name: "provenance alone (a legacy overlay) is not ownership",
			images: []storeImage{{
				id: fixtureID(), refs: []string{"ctxloom-agent:latest"}, created: pruneNow.Add(-30 * day),
				labels: map[string]string{provenanceLabel: "p"},
			}},
			want:    map[string]KeepReason{},
			unowned: []string{"ctxloom-agent:latest"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeImageStore{images: tc.images}
			if tc.containers != nil {
				s.containers = tc.containers(tc.images)
			}
			plan, _ := planFor(t, s, tc.live...)
			assert.Equal(t, tc.want, verdictsByRef(plan))
			assert.Equal(t, tc.unowned, plan.Unowned)
			assert.Empty(t, s.rmis(), "planning must never remove anything")
		})
	}
}

// TestApplyImagePrune_RemovesOnlySupersededNeverForced: apply removes exactly
// the superseded images, one unforced rmi each, and never an unowned one.
func TestApplyImagePrune_RemovesOnlySupersededNeverForced(t *testing.T) {
	s := &fakeImageStore{images: []storeImage{
		composedImg("ctxloom-agent-mock:v1-cC-S", "S", "C", 5*day, ""),
		composedImg("ctxloom-agent-mock:v2-cC-S", "S", "C", 3*day, ""),
		unlabelledImg("ctxloom-agent-base:latest"),
	}}
	plan, rt := planFor(t, s)
	assert.Equal(t, int64(40e6), plan.Reclaimable(), "unique-layer bytes of the one superseded image")

	res := ApplyImagePrune(context.Background(), rt, plan)
	require.Len(t, res.Removed, 1)
	assert.Equal(t, "ctxloom-agent-mock:v1-cC-S", res.Removed[0].name())
	assert.Empty(t, res.Failed)
	assert.Equal(t, [][]string{{"rmi", "ctxloom-agent-mock:v1-cC-S"}}, s.rmis())
	for _, c := range s.calls {
		assert.NotContains(t, c, "-f", "no argv may force: %v", c)
		assert.NotContains(t, c, "--force", "no argv may force: %v", c)
	}
}

// TestApplyImagePrune_FailureIsRecordedAndOthersProceed: one refused rmi is
// reported with its error, and the sweep carries on to the rest.
func TestApplyImagePrune_FailureIsRecordedAndOthersProceed(t *testing.T) {
	s := &fakeImageStore{
		images: []storeImage{
			composedImg("ctxloom-agent-mock:v1-cC-S", "S", "C", 6*day, ""),
			composedImg("ctxloom-agent-mock:v2-cC-S", "S", "C", 5*day, ""),
			composedImg("ctxloom-agent-mock:v3-cC-S", "S", "C", 4*day, ""),
		},
		failRmi: map[string]bool{"ctxloom-agent-mock:v1-cC-S": true},
	}
	plan, rt := planFor(t, s)
	res := ApplyImagePrune(context.Background(), rt, plan)
	require.Len(t, res.Failed, 1)
	assert.Equal(t, "ctxloom-agent-mock:v1-cC-S", res.Failed[0].Image.name())
	assert.Error(t, res.Failed[0].Err)
	require.Len(t, res.Removed, 1)
	assert.Equal(t, "ctxloom-agent-mock:v2-cC-S", res.Removed[0].name())
	assert.Len(t, s.rmis(), 2, "the failure did not stop the sweep")
}

// TestApplyImagePrune_DanglingRemovedByID: a superseded image left untagged by
// a rebuild over its tag is removed by ID.
func TestApplyImagePrune_DanglingRemovedByID(t *testing.T) {
	old := composedImg("x", "S", "C", 5*day, "")
	old.refs = nil
	s := &fakeImageStore{images: []storeImage{old, composedImg("ctxloom-agent-mock:v1-cC-S", "S", "C", 3*day, "")}}
	plan, rt := planFor(t, s)
	ApplyImagePrune(context.Background(), rt, plan)
	assert.Equal(t, [][]string{{"rmi", old.id}}, s.rmis())
}

// TestPlanImagePrune_HostHasNothing: Host runs no probe at all.
func TestPlanImagePrune_HostHasNothing(t *testing.T) {
	s := &fakeImageStore{}
	s.install(t)
	plan, err := PlanImagePrune(context.Background(), Host{}, ImagePruneOptions{Now: pruneNow})
	require.NoError(t, err)
	assert.Empty(t, plan.Verdicts)
	assert.Empty(t, s.calls)
}

func TestParseHumanSize(t *testing.T) {
	for in, want := range map[string]int64{"0B": 0, "42.1MB": 42_100_000, "1.5GB": 1_500_000_000, "512kB": 512_000, "8.716MB": 8_716_000} {
		got, ok := parseHumanSize(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	_, ok := parseHumanSize("N/A")
	assert.False(t, ok)
}

// TestParsePodmanUniqueSizes reads a captured `podman system df -v` table.
func TestParsePodmanUniqueSizes(t *testing.T) {
	out := `Images space usage:

REPOSITORY                TAG         IMAGE ID      CREATED     SIZE        SHARED SIZE  UNIQUE SIZE  CONTAINERS
docker.io/library/alpine  latest      320994c3b997  7 days      8.716MB     8.716MB      0B           0
<none>                    <none>      2121600d9199  45 hours    53.27MB     0B           53.27MB      0

Containers space usage:

CONTAINER ID  IMAGE  COMMAND  LOCAL VOLUMES  SIZE  CREATED  STATUS  NAMES
`
	assert.Equal(t, map[string]int64{"320994c3b997": 0, "2121600d9199": 53_270_000}, parsePodmanUniqueSizes(out))
}
