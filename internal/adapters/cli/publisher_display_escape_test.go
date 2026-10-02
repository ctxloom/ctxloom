package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
)

// hostileDisplay is one publisher-authored value carrying every family of
// terminal control the termsafe seam defuses: a CSI erase-line, an OSC
// window-title set terminated by BEL, a carriage return, a backspace, NUL, and
// the single-rune C1 CSI (U+009B) that needs no ESC at all.
const hostileDisplay = "pub\x1b[2K\x1b]0;owned\x07\r\x08\x00\u009bx"

// hostileDisplayMark is the caret form of hostileDisplay's leading CSI. Its
// presence proves the value reached the output ESCAPED rather than dropped —
// a renderer that silently omitted the field would otherwise pass.
const hostileDisplayMark = "pub^[[2K"

// assertTerminalInert fails on any rune a terminal would act on: C0 other
// than newline and tab, DEL, the C1 block, and bytes that are not UTF-8.
func assertTerminalInert(t *testing.T, out string) {
	t.Helper()
	for i, r := range out {
		switch {
		case r == utf8.RuneError:
			t.Errorf("invalid UTF-8 at byte %d reached the terminal", i)
		case r == '\n' || r == '\t':
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			t.Errorf("raw control %U at byte %d reached the terminal", r, i)
		}
	}
}

// TestPublisherDisplayPaths_ControlBytesAreEscaped drives every text renderer
// that shows publisher-authored bytes with a hostile value in each publisher
// field, and asserts nothing a terminal would execute survives. Each row is one
// display path; its field set is the publisher-supplied fields that path shows.
func TestPublisherDisplayPaths_ControlBytesAreEscaped(t *testing.T) {
	h := hostileDisplay
	cases := []struct {
		name   string
		render func(t *testing.T) string
	}{
		{"bundle list", func(t *testing.T) string {
			var buf bytes.Buffer
			require.NoError(t, renderBundleList(&buf, []*bundles.BundleInfo{{
				Name: h, Version: h, Description: h, Tags: []string{h},
				Retracted: true, RetractedReason: h,
			}}))
			return buf.String()
		}},
		{"bundle show", func(t *testing.T) string {
			var buf bytes.Buffer
			require.NoError(t, renderBundleShow(&buf, &bundles.Bundle{
				Name: h, Version: h, Author: h, Description: h, Tags: []string{h},
				Notes: h + "\nsecond " + h, Path: "/proj/b",
				MCP: map[string]bundles.BundleMCP{h: {
					Command: h, Args: []string{h}, Env: map[string]string{h: h},
					Notes: h, Installation: h,
				}},
				Fragments: map[string]bundles.BundleFragment{h: {
					ItemBody: bundles.ItemBody{Tags: []string{h}, Content: h},
				}},
				Commands: map[string]bundles.BundleCommand{h: {
					ItemBody: bundles.ItemBody{Tags: []string{h}}, Description: h,
				}},
			}))
			return buf.String()
		}},
		{"skill list", func(t *testing.T) string {
			cmd, buf := testCmd()
			require.NoError(t, printSkillList(cmd, []operations.SkillEntry{{
				Name: h, Description: h, Tags: []string{h}, Source: h, FileCount: 1,
			}}, "", 1))
			return buf.String()
		}},
		{"skill show", func(t *testing.T) string {
			var buf bytes.Buffer
			require.NoError(t, renderSkillShow(&buf, &operations.GetSkillResult{
				Name: h, Bundle: h, Description: h, License: h, Compatibility: h,
				AllowedTools: []string{h}, Body: h + "\n\nbody " + h,
				Files: []operations.SkillFileEntry{{Path: h, SHA256: "abc", Mode: "0644"}},
			}))
			return buf.String()
		}},
		{"skill export", func(t *testing.T) string {
			var buf bytes.Buffer
			renderSkillExport(&buf, &operations.ExportSkillResult{Name: h, ZipPath: h + ".zip", Bytes: 1})
			return buf.String()
		}},
		{"skill import", func(t *testing.T) string {
			var buf bytes.Buffer
			renderSkillImport(&buf, &operations.ImportSkillResult{
				Name: h, Bundle: "b", Dir: "/proj/skills/" + h, FileCount: 1,
				SignatureState: "unverified: " + h,
			})
			return buf.String()
		}},
		{"profile show", func(t *testing.T) string {
			var buf bytes.Buffer
			require.NoError(t, renderProfileShow(&buf, &operations.GetProfileResult{
				Name: h, Path: "/proj/p", Bundle: h, Description: h, LLM: h,
				Parents: []string{h}, Bundles: []string{h}, Tags: []string{h},
				Variables:        map[string]string{h: h},
				ExcludeFragments: []string{h}, ExcludeMCP: []string{h},
			}, false))
			return buf.String()
		}},
		{"profile materialize --diff", func(t *testing.T) string {
			var buf bytes.Buffer
			require.NoError(t, renderMaterializeDiff(&buf, "p", profileMaterializeDiffJSON{
				ComparedTo: "theirs.md", Diff: "--- a\n+++ b\n-" + h + "\n+" + h + "\n",
			}))
			return buf.String()
		}},
		{"deps list", func(t *testing.T) string {
			var buf bytes.Buffer
			require.NoError(t, renderDepsList(&buf, &depsListing{Deps: []installedDep{{
				Name: h, SHA: "0123456789abcdef", Constraint: h, URL: h,
			}}, Count: 1}))
			return buf.String()
		}},
		{"deps pull summary", func(t *testing.T) string {
			var buf bytes.Buffer
			renderPullSummary(&buf, &operations.SyncDependenciesResult{
				Total: 2, Errors: 1,
				Retracted: []operations.SyncItem{{Reference: h, Error: h}},
				Failed:    []operations.SyncItem{{Reference: h, Error: h}},
			})
			return buf.String()
		}},
		{"deps pull reconcile", func(t *testing.T) string {
			var buf bytes.Buffer
			renderReconcile(&buf, operations.ReconcilePlan{
				Gone:        []trust.BundleKey{trust.BundleKey(h)},
				Unreachable: []operations.UncheckedRemote{{URL: h, Refs: []trust.BundleKey{trust.BundleKey(h)}, Reason: h}},
			})
			return buf.String()
		}},
		{"deps verify-corpus", func(t *testing.T) string {
			var buf bytes.Buffer
			reportCorpus(&buf, operations.CorpusReport{
				RemotesConfigured: 1,
				Violations: []operations.CorpusViolation{{
					Bundle: operations.CorpusBundle{Remote: h, URL: h, Path: h}, Err: errors.New(h),
				}},
				Gaps: []operations.CorpusGap{{Remote: h, URL: h, Path: h, Err: errors.New(h)}},
			})
			return buf.String()
		}},
		{"doctor", func(t *testing.T) string {
			var buf bytes.Buffer
			require.NoError(t, renderDoctorReport(&buf, operations.DoctorReport{Checks: []operations.DoctorCheck{{
				Marker: "DOCTOR-CHECK-X", Status: operations.DoctorOK, Detail: "bundle " + h + " failed",
			}}}))
			return buf.String()
		}},
		{"search local", func(t *testing.T) string {
			var buf bytes.Buffer
			printLocalResults(&buf, []operations.SearchResult{{
				Type: "fragment", Name: h, Tags: []string{h}, Source: h,
			}})
			return buf.String()
		}},
		{"search remote", func(t *testing.T) string {
			var buf bytes.Buffer
			printRemoteResults(&buf, []operations.SearchRemoteEntry{{
				Type: "bundle", Remote: "origin", Name: h, Tags: []string{h},
			}})
			return buf.String()
		}},
		{"remote show", func(t *testing.T) string {
			var buf bytes.Buffer
			renderRemoteBrowse(&buf, "bundle", &operations.BrowseRemoteResult{
				Remote: "origin", URL: "https://example.test/r",
				Items: []operations.BrowseItemEntry{{PullRef: h, Path: "bundles/x"}}, Count: 1,
			})
			return buf.String()
		}},
		{"bundle trust", func(t *testing.T) string {
			var buf bytes.Buffer
			renderItemTrust(&buf, &operations.SetItemTrustResult{
				Ref: h, RepoURL: h, Store: "user", KeyFingerprint: "SHA256:abc",
			})
			return buf.String()
		}},
		{"bundle reject", func(t *testing.T) string {
			var buf bytes.Buffer
			renderItemReject(&buf, &operations.SetBlacklistResult{
				Ref: h, RepoURL: h, Store: "user", KeyFingerprint: "SHA256:abc",
				ContentForms: []string{"raw"},
			})
			return buf.String()
		}},
		{"bundle distill", func(t *testing.T) string {
			var buf bytes.Buffer
			w := errwriter.New(&buf)
			printDistillItems(w, []operations.DistillBundleItem{
				{Kind: "fragment", Name: h, Status: operations.DistillStatusSkipped, Reason: h},
				{Kind: "fragment", Name: h, Status: operations.DistillStatusPlanned},
				{Kind: "fragment", Name: h, Status: operations.DistillStatusDistilled, ModelID: h},
			})
			printDistillInvalidatedApprovals(w, []string{h})
			require.NoError(t, w.Err())
			return buf.String()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.render(t)
			assert.Contains(t, out, hostileDisplayMark,
				"the publisher value must reach the output escaped, not be dropped")
			assertTerminalInert(t, out)
			assert.False(t, strings.Contains(out, "\x1b"), "no raw ESC may reach the terminal")
		})
	}
}
