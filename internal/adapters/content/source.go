package content

import (
	"fmt"
	"path"
	"slices"
	"sort"
)

// treeSource is a Source over one candidate group in an authored directory
// tree: a fixed, sorted set of bundle-relative paths resolved against the
// bundle root.
//
// The path set is fixed at construction rather than re-globbed on each call,
// which is what makes grouping decidable: Detect and Decode see exactly the
// files the walker grouped together, including dot-prefixed sidecars that a
// glob would have silently dropped.
type treeSource struct {
	tfs   TreeFS
	root  string   // store-relative slash path of the bundle root
	paths []string // bundle-relative, forward slashes, sorted, deduplicated
}

func newTreeSource(tfs TreeFS, root string, paths []string) *treeSource {
	cleaned := slices.Clone(paths)
	sort.Strings(cleaned)
	cleaned = slices.Compact(cleaned)
	return &treeSource{tfs: tfs, root: root, paths: cleaned}
}

func (s *treeSource) List() ([]string, error) {
	return slices.Clone(s.paths), nil
}

func (s *treeSource) Open(relPath string) ([]byte, error) {
	if !slices.Contains(s.paths, relPath) {
		// Refusing an ungrouped path is what keeps a Source scoped to its own
		// item: a decoder cannot reach a sibling item's bytes, so it cannot
		// attest bytes that are not components of the item being read.
		return nil, fmt.Errorf("%w: %q is not a component of this item", ErrBadPath, relPath)
	}
	if err := validateDigestPath(relPath); err != nil {
		return nil, err
	}
	data, err := s.tfs.ReadFile(path.Join(s.root, relPath))
	if err != nil {
		return nil, fmt.Errorf("content: reading %q: %w", relPath, err)
	}
	return data, nil
}

// memSource is a Source over an in-memory component set. It exists so a caller
// (or a test) can run Detect/Decode over components that never touched a
// filesystem — the same shape the document-backed and remote backends will use.
type memSource struct {
	files map[string][]byte
}

func newMemSource(components []Component) *memSource {
	files := make(map[string][]byte, len(components))
	for _, c := range components {
		files[c.Path] = c.Bytes
	}
	return &memSource{files: files}
}

func (s *memSource) List() ([]string, error) {
	out := make([]string, 0, len(s.files))
	for p := range s.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func (s *memSource) Open(relPath string) ([]byte, error) {
	data, ok := s.files[relPath]
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a component of this item", ErrBadPath, relPath)
	}
	return slices.Clone(data), nil
}
