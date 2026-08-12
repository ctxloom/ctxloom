package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Name is the parsed form of a spool filename:
// "<unixnano %020d>.<seq %08d>.<writer>.md".
//
// The filename IS the message identity AND its sort order: zero-padded
// fixed-width unixnano makes lexicographic order chronological, and the
// per-writer seq is the uniqueness tiebreak that also survives a clock step
// backwards. `ls` is therefore the mailbox inspector, and a sweep gets
// arrival order from readdir+sort without reading a byte of any file.
type Name struct {
	// Nanos is the write stamp in Unix nanoseconds.
	Nanos int64
	// Seq is the writer's monotonic counter for this file.
	Seq uint64
	// Writer identifies who wrote it ("coord", a harp, ...). Kept for
	// debuggability and as the guard if the single-writer-per-direction
	// invariant ever breaks, not because delivery depends on it.
	Writer string
}

// MessageFileExt is the extension every spool message file carries.
const MessageFileExt = ".md"

// String renders the filename.
func (n Name) String() string {
	return fmt.Sprintf("%020d.%08d.%s%s", n.Nanos, n.Seq, n.Writer, MessageFileExt)
}

// Stem returns the filename without its extension — the message ID.
func (n Name) Stem() string {
	return strings.TrimSuffix(n.String(), MessageFileExt)
}

// ParseName parses a spool filename. It is strict: anything that is not a
// message file (a stray dotfile, an editor backup, a directory name that
// slipped through) must be reported as a problem by the caller, never
// silently skipped.
func ParseName(name string) (Name, error) {
	if err := ValidateName(name); err != nil {
		return Name{}, err
	}
	if !strings.HasSuffix(name, MessageFileExt) {
		return Name{}, fmt.Errorf("spool: %q is not a message file (want a %s file named <unixnano>.<seq>.<writer>%s)", name, MessageFileExt, MessageFileExt)
	}
	stem := strings.TrimSuffix(name, MessageFileExt)
	parts := strings.SplitN(stem, ".", 3)
	if len(parts) != 3 {
		return Name{}, fmt.Errorf("spool: %q is not a message file (want <unixnano>.<seq>.<writer>%s)", name, MessageFileExt)
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Name{}, fmt.Errorf("spool: %q has a non-numeric timestamp field %q: %w", name, parts[0], err)
	}
	seq, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return Name{}, fmt.Errorf("spool: %q has a non-numeric sequence field %q: %w", name, parts[1], err)
	}
	if err := validateWriterID(parts[2]); err != nil {
		return Name{}, fmt.Errorf("spool: %q: %w", name, err)
	}
	return Name{Nanos: nanos, Seq: seq, Writer: parts[2]}, nil
}

// validateWriterID refuses a writer token that would make a filename
// ambiguous to ParseName (a dot would move the field boundaries) or that
// would escape the directory.
func validateWriterID(id string) error {
	if id == "" {
		return fmt.Errorf("writer id is required")
	}
	if strings.ContainsAny(id, `./\:`) {
		return fmt.Errorf("invalid writer id %q: must not contain a dot or a path separator", id)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid writer id %q: must not contain control characters", id)
		}
	}
	return nil
}

// Writer publishes messages into ONE direction of ONE session's spool.
//
// Single writer per direction is the design's concurrency model (in/ is
// written only by the coordinator, out/ only by that harp's runner), which is
// what makes ordering and cursors trivial. The mutex and seq here are not a
// hedge against that invariant breaking: they exist because one writer can
// legitimately publish twice inside the same nanosecond, and two files with
// the same name would be one lost message.
type Writer struct {
	mapper PathMapper
	harp   string
	dir    Dir
	id     string

	mu   sync.Mutex
	seq  uint64
	root string
}

// NewWriter returns a writer for harp's dir (DirIn or DirOut — the
// consumed/withdrawn directories are reached by rename, never written into
// directly), publishing under the given writer id.
//
// The sequence counter is re-seeded from the highest seq already on disk
// across the direction and its consumed/withdrawn siblings, so a restarted
// process cannot reissue a name a still-present file already holds.
func NewWriter(m PathMapper, harp string, dir Dir, writerID string) (*Writer, error) {
	if m == nil {
		return nil, fmt.Errorf("spool: a PathMapper is required")
	}
	if dir != DirIn && dir != DirOut {
		return nil, fmt.Errorf("spool: %q is not a writable direction (write to %q or %q; consumed and withdrawn are reached by rename)", string(dir), string(DirIn), string(DirOut))
	}
	if err := validateWriterID(writerID); err != nil {
		return nil, fmt.Errorf("spool: %w", err)
	}
	if err := EnsureDirs(m, harp); err != nil {
		return nil, err
	}
	root, err := Root(m, harp)
	if err != nil {
		return nil, err
	}
	w := &Writer{mapper: m, harp: harp, dir: dir, id: writerID, root: root}
	seq, err := w.highestSeq()
	if err != nil {
		return nil, err
	}
	w.seq = seq
	return w, nil
}

// highestSeq scans the direction and its terminal siblings for the largest
// seq already used. Names that do not parse are ignored HERE (they are
// reported loudly by Sweep, which is the reader's job); seeding must never
// fail because someone dropped a note in the directory.
func (w *Writer) highestSeq() (uint64, error) {
	dirs := []Dir{w.dir}
	if consumed, err := w.dir.Consumed(); err == nil {
		dirs = append(dirs, consumed)
	}
	if withdrawn, err := w.dir.Withdrawn(); err == nil {
		dirs = append(dirs, withdrawn)
	}
	var highest uint64
	for _, d := range dirs {
		path := filepath.Join(w.root, filepath.FromSlash(string(d)))
		entries, err := os.ReadDir(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, fmt.Errorf("spool: reading %s to re-seed the writer sequence: %w", path, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			parsed, err := ParseName(e.Name())
			if err != nil {
				continue
			}
			if parsed.Seq > highest {
				highest = parsed.Seq
			}
		}
	}
	return highest, nil
}

// Dir reports the direction this writer publishes into.
func (w *Writer) Dir() Dir { return w.dir }

// ID reports the writer token stamped into every filename it produces.
func (w *Writer) ID() string { return w.id }

// Write publishes msg and returns the ref of the published file.
//
// The protocol is write-staging-file, fsync, rename, fsync-dir. A half-written
// file is never observable under its final name, because it never HAS its
// final name until it is complete and durable: readers see the rename or
// nothing. The staging file lives in the spool's own tmp/ dir, a sibling of
// the target on the same filesystem, so the rename is atomic by construction
// rather than by luck.
//
// msg is stamped in place with its ID (the filename stem), Created (if unset)
// and V (if unset), so the caller's copy agrees with the bytes on disk.
func (w *Writer) Write(msg *Message) (Ref, error) {
	if msg == nil {
		return Ref{}, fmt.Errorf("spool: a message is required")
	}
	if msg.Kind == "" {
		return Ref{}, fmt.Errorf("spool: refusing to write a message with no kind: the frontmatter kind is how every reader routes it")
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.seq++
	name := Name{Nanos: time.Now().UTC().UnixNano(), Seq: w.seq, Writer: w.id}
	ref := Ref{Harp: w.harp, Dir: w.dir, Name: name.String()}
	final, err := w.mapper.Resolve(ref)
	if err != nil {
		return Ref{}, err
	}

	if msg.Created.IsZero() {
		msg.Created = time.Unix(0, name.Nanos).UTC()
	}
	if msg.V == 0 {
		msg.V = CurrentVersion
	}
	msg.ID = name.Stem()
	data, err := msg.Encode()
	if err != nil {
		return Ref{}, err
	}

	tmp := filepath.Join(w.root, tmpDirName, name.String())
	if err := writeAndSync(tmp, data); err != nil {
		return Ref{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return Ref{}, fmt.Errorf("spool: publishing %s: %w", ref, err)
	}
	if err := syncDir(filepath.Dir(final)); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

// writeAndSync writes data to path and fsyncs the file before returning, so
// the rename that follows publishes durable bytes rather than a promise.
func writeAndSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm)
	if err != nil {
		return fmt.Errorf("spool: creating staging file %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("spool: writing staging file %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("spool: syncing staging file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("spool: closing staging file %s: %w", path, err)
	}
	return nil
}

// syncDir fsyncs a directory so a rename into it survives a crash. A
// filesystem that refuses the operation (some do for directories) is not an
// error worth failing a delivery over — the bytes are already durable and the
// entry is already visible — so an EINVAL-class refusal is tolerated while a
// missing directory is not.
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("spool: opening %s to sync: %w", path, err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		if os.IsPermission(err) || errorIsInvalid(err) {
			return nil
		}
		return fmt.Errorf("spool: syncing %s: %w", path, err)
	}
	return nil
}

func errorIsInvalid(err error) bool {
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		return false
	}
	return errors.Is(pathErr.Err, os.ErrInvalid) || strings.Contains(pathErr.Err.Error(), "invalid argument")
}
