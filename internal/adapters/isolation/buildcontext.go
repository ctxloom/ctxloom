package isolation

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// This file keys an on-disk base stage (a devcontainer's build.dockerfile, or
// a compose service's build) by EVERY input that changes the image it builds,
// not by its Dockerfile bytes alone. The build is
// `<runtime> build [--build-arg K=V ...] -f <Dockerfile> <context>` (see
// ociRuntime.buildArgs) with no --target, so the inputs are exactly: the
// Dockerfile, the build args, and the files of the context the runtime sends.
// A key that missed any of them reused a stale image silently: a changed
// build.args value, or an edited file the Dockerfile COPYs, kept the old tag,
// which was present and so was run as-is.
//
// CONTEXT HASHING — the choice and why:
//   - Stat-based keys (mtime/size) are unsafe: an edit that keeps the size
//     within one mtime tick, a `cp -p`/`tar -p` restore, or a checkout that
//     resets mtimes changes the image without changing the key.
//   - Parsing the Dockerfile for COPY/ADD sources is unsafe too: globs,
//     ARG-expanded sources and RUN --mount=type=bind all read the context in
//     ways a parser short of BuildKit's own would miss.
//   - So the key is the CONTENT (and mode, and symlink target) of every file
//     the runtime would send, with the runtime's own ignore rules applied by
//     moby/patternmatcher, the matcher docker's CLI uses. It is bounded by
//     contextHashBudget: once that many bytes have been read, any further
//     file is keyed by its size and mtime instead, and that degrade is
//     announced once (WarnOnce) with the remedy (.dockerignore, or a narrower
//     build.context). The budget exists because the key is recomputed on
//     every container start, not only on a build; a context too big to read
//     within it is one the runtime must also send in full on each build, so
//     the remedy helps both.
var contextHashBudget int64 = 64 << 20

// identity is the key material for this base stage's tags (baseImageTagFor,
// composedIdentity) and slot label: the embedded Containerfile bytes for a
// stage with no on-disk Dockerfile (the default base, a synthetic FROM), so
// those keys are unchanged; otherwise a digest over the Dockerfile, the sorted
// build args, and the build context (see the file comment). It is the ONE
// place a base stage's identity is resolved, so the tag a run looks up and the
// tag a build produces cannot be keyed off different notions of the base.
func (b *baseStage) identity() ([]byte, error) {
	if b.path == "" {
		return b.containerfile, nil
	}
	dockerfile, err := os.ReadFile(b.path)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	writeKeyField(h, "dockerfile", dockerfile)
	// buildArgs is already sorted by its one producer (devcontainerBuildStage).
	for _, a := range b.buildArgs {
		writeKeyField(h, "arg", []byte(a))
	}
	if err := hashBuildContext(h, b.contextDir(), b.path); err != nil {
		return nil, fmt.Errorf("build context %s: %w", b.contextDir(), err)
	}
	return []byte("ctxloom-build-inputs/v1:" + hex.EncodeToString(h.Sum(nil))), nil
}

// contextDir is the directory an on-disk base stage builds from: the
// configured context, else the Dockerfile's own directory.
func (b *baseStage) contextDir() string {
	if b.context != "" {
		return b.context
	}
	return filepath.Dir(b.path)
}

// writeKeyField writes one length-prefixed, tagged field into the key, so no
// two different input sets can concatenate to the same byte stream.
func writeKeyField(h hash.Hash, tag string, val []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(val)))
	h.Write([]byte(tag))
	h.Write([]byte{0})
	h.Write(n[:])
	h.Write(val)
}

// contextIgnore is the exclusion rule for the files a build sends. docker and
// podman read DIFFERENT ignore files: docker (BuildKit) uses
// <Dockerfile>.dockerignore when it exists, else <context>/.dockerignore;
// podman uses <context>/.containerignore when it exists, else .dockerignore.
// Which runtime builds is not known where the key is computed, so a file is
// excluded only when BOTH runtimes' chosen files exclude it. Erring that way
// costs at worst a spurious rebuild; erring the other way would run a stale
// image.
type contextIgnore struct {
	docker, podman *patternmatcher.PatternMatcher
}

// loadContextIgnore reads each runtime's ignore file. The ignore files' own
// bytes are deliberately NOT keyed: they change the image only by changing
// which files are sent, and the walk keys exactly that set, so keying them
// too would only add rebuilds for edits that send the same files.
func loadContextIgnore(ctxDir, dockerfile string) (contextIgnore, error) {
	pick := func(paths ...string) (*patternmatcher.PatternMatcher, error) {
		for _, p := range paths {
			raw, err := os.ReadFile(p)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			patterns, err := ignorefile.ReadAll(bytes.NewReader(raw))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			return patternmatcher.New(patterns)
		}
		return nil, nil
	}
	dockerIgnore := filepath.Join(ctxDir, ".dockerignore")
	d, err := pick(dockerfile+".dockerignore", dockerIgnore)
	if err != nil {
		return contextIgnore{}, err
	}
	p, err := pick(filepath.Join(ctxDir, ".containerignore"), dockerIgnore)
	if err != nil {
		return contextIgnore{}, err
	}
	return contextIgnore{docker: d, podman: p}, nil
}

// excluded reports whether neither runtime sends rel (slash-separated).
func (ci contextIgnore) excluded(rel string) (bool, error) {
	if ci.docker == nil || ci.podman == nil {
		return false, nil
	}
	d, err := ci.docker.MatchesOrParentMatches(rel)
	if err != nil || !d {
		return false, err
	}
	return ci.podman.MatchesOrParentMatches(rel)
}

// mayReinclude reports whether an excluded directory can still hold a sent
// file (a "!pattern" exception), so the walk must descend into it.
func (ci contextIgnore) mayReinclude() bool {
	return ci.docker.Exclusions() || ci.podman.Exclusions()
}

// hashBuildContext writes the sent files of ctxDir into h in lexical walk
// order: each file's relative path and mode, then its content (or, past
// contextHashBudget, its size and mtime), or a symlink's target.
func hashBuildContext(h hash.Hash, ctxDir, dockerfile string) error {
	ign, err := loadContextIgnore(ctxDir, dockerfile)
	if err != nil {
		return err
	}
	w := &contextHasher{h: h, ctxDir: ctxDir, ign: ign, budget: contextHashBudget}
	err = filepath.WalkDir(ctxDir, w.visit)
	if w.degraded {
		clidiag.WarnOnce("ctxloom", "the devcontainer build context %s holds more than %d MiB the build sends; files past that are keyed by size and modification time only, so an edit that keeps both will not rebuild the agent image (add a .dockerignore, or narrow build.context)", ctxDir, contextHashBudget>>20)
	}
	return err
}

// contextHasher is one hashBuildContext walk: the hash it writes into, the
// ignore rules it applies, and the content budget it has left.
type contextHasher struct {
	h        hash.Hash
	ctxDir   string
	ign      contextIgnore
	budget   int64
	degraded bool // some file past the budget was keyed by size and mtime
}

// visit is the filepath.WalkDir callback: it hashes each entry the build
// sends and prunes excluded directories no exception can reach into.
func (w *contextHasher) visit(p string, d fs.DirEntry, werr error) error {
	if werr != nil {
		return werr
	}
	rel, err := filepath.Rel(w.ctxDir, p)
	if err != nil || rel == "." {
		return err
	}
	rel = filepath.ToSlash(rel)
	skip, err := w.ign.excluded(rel)
	if err != nil {
		return err
	}
	if skip {
		return w.skipped(d)
	}
	info, err := d.Info()
	if err != nil {
		return err
	}
	return w.hashEntry(p, rel, info)
}

// skipped is the walk's answer for an excluded entry: prune a directory
// unless a "!pattern" exception may still send something under it.
func (w *contextHasher) skipped(d fs.DirEntry) error {
	if d.IsDir() && !w.ign.mayReinclude() {
		return filepath.SkipDir
	}
	return nil
}

// hashEntry writes one sent entry: its path and mode, then a symlink's target
// or a regular file's content (or, past the budget, its size and mtime).
func (w *contextHasher) hashEntry(p, rel string, info fs.FileInfo) error {
	writeKeyField(w.h, "path", []byte(rel))
	writeKeyField(w.h, "mode", []byte(info.Mode().String()))
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		writeKeyField(w.h, "link", []byte(target))
	case info.Mode().IsRegular():
		if info.Size() > w.budget {
			w.degraded = true
			writeKeyField(w.h, "stat", []byte(fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())))
			return nil
		}
		w.budget -= info.Size()
		return hashFileContent(w.h, p)
	}
	return nil
}

// hashFileContent streams one file's content into h, length-prefixed by the
// bytes actually read.
func hashFileContent(h hash.Hash, p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sub := sha256.New()
	if _, err := io.Copy(sub, f); err != nil {
		return err
	}
	writeKeyField(h, "content", sub.Sum(nil))
	return nil
}
