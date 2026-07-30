package resources

import (
	"testing"
	"testing/fstest"
)

// listingFixture exercises every shape the two builtin listers have to
// classify: a normal match, a non-matching extension, a directory whose name
// ends in the extension, a name that is the extension without its dot, and a
// file whose ENTIRE name is the extension. That last one is the interesting
// case — the index arithmetic both listers used ("len(name) > 3 &&
// name[len-3:] == \".md\"") rejects it, while a naive
// strings.HasSuffix/TrimSuffix rewrite accepts it and yields an EMPTY name.
func listingFixture(ext string) fstest.MapFS {
	return fstest.MapFS{
		"d/real" + ext:            {Data: []byte("x")},
		"d/second" + ext:          {Data: []byte("y")},
		"d/other.txt":             {Data: []byte("x")},
		"d/dir" + ext + "/in.txt": {Data: []byte("x")},
		"d/" + ext[1:]:            {Data: []byte("x")},
		"d/" + ext:                {Data: []byte("x")},
	}
}

func TestListEmbeddedNames(t *testing.T) {
	for _, ext := range []string{".md", ".yaml"} {
		t.Run(ext, func(t *testing.T) {
			got, err := listEmbeddedNames(listingFixture(ext), "d", ext)
			if err != nil {
				t.Fatalf("listEmbeddedNames: %v", err)
			}
			want := []string{"real", "second"}
			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("got %v, want %v", got, want)
				}
			}
			for _, n := range got {
				if n == "" {
					t.Errorf("listEmbeddedNames returned an EMPTY name from %v; a file whose whole name is %q is not a named item", got, ext)
				}
			}
		})
	}
}
