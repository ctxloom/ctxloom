//go:build treesitter

package compression

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSignatureExtractors_NestedMatchesTopLevel is the parity gate across each
// language's PAIR of signature extractors: the one walking a TOP-LEVEL
// declaration and the one walking the same declaration NESTED inside a class or
// impl block. A signature is the whole point of a distilled fragment, so a
// nested extractor that recognizes fewer node kinds than its top-level twin
// does not merely format differently — it silently DROPS modifiers, turning
// `async fn` into `fn` and dropping `where` bounds, and the reader of the
// distilled output has no way to tell.
func TestSignatureExtractors_NestedMatchesTopLevel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ct      ContentType
		source  string
		mustHave []string
	}{
		{
			name: "rust impl method keeps modifiers and bounds",
			ct:   ContentTypeRust,
			source: `impl Widget {
    pub async unsafe fn fetch<T>(&self, id: T) -> Result<u32> where T: Copy {
        unimplemented!()
    }
}
`,
			mustHave: []string{"pub", "async", "unsafe", "fn fetch", "<T>", "-> Result<u32>", "where T: Copy"},
		},
		{
			name: "rust top-level fn keeps modifiers and bounds",
			ct:   ContentTypeRust,
			source: `pub async unsafe fn fetch<T>(id: T) -> Result<u32> where T: Copy {
    unimplemented!()
}
`,
			mustHave: []string{"pub", "async", "unsafe", "fn fetch", "<T>", "-> Result<u32>", "where T: Copy"},
		},
		{
			name: "python class method keeps async and annotations",
			ct:   ContentTypePython,
			source: `class Widget:
    async def fetch(self, id: int) -> str:
        return "x"
`,
			mustHave: []string{"class Widget", "async def fetch", "(self, id: int)", "-> str"},
		},
		{
			name: "python top-level def keeps async and annotations",
			ct:   ContentTypePython,
			source: `async def fetch(id: int) -> str:
    return "x"
`,
			mustHave: []string{"async def fetch", "(id: int)", "-> str"},
		},
		{
			name: "java method and constructor both keep their signatures",
			ct:   ContentTypeJava,
			source: `public class Widget {
    public Widget(int id) { this.id = id; }
    public static <T> String fetch(int id) throws Exception { return "x"; }
}
`,
			mustHave: []string{"public Widget(int id)", "public static <T> String fetch(int id) throws Exception"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewCodeCompressor().Compress(context.Background(), tc.ct, tc.source)
			require.NoError(t, err)
			for _, want := range tc.mustHave {
				if !strings.Contains(got.Content, want) {
					t.Errorf("distilled signature dropped %q\n--- got ---\n%s", want, got.Content)
				}
			}
		})
	}
}
