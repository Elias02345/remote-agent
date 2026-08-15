package files

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TusVersion is the protocol version this implements (tus.io core plus the
// creation extension). The full spec has more extensions; the core is what
// Section 8.2 actually asks for — chunked, resumable uploads — so the rest is
// not built.
const TusVersion = "1.0.0"

// ErrOffsetConflict is returned when a PATCH does not continue exactly where
// the stored upload left off.
var ErrOffsetConflict = errors.New("upload offset conflict")

// ErrChecksumMismatch is returned when the assembled file does not hash to
// what the client declared.
var ErrChecksumMismatch = errors.New("checksum mismatch")

// ErrMissingChecksum is returned when an upload is created without a usable
// SHA-256.
var ErrMissingChecksum = errors.New("a lowercase 64-character sha256 is required in Upload-Metadata")

// ErrTooLarge is returned for an upload that declares more than MaxUploadBytes.
var ErrTooLarge = errors.New("upload exceeds the maximum permitted size")

// MaxUploadBytes bounds a single upload.
//
// Without a bound, Upload-Length is a number the client picks and the daemon
// reserves disk for — an authenticated device can fill the filesystem, and a
// full filesystem takes the SQLite database and every terminal with it. 16 GiB
// is far past any real transfer over this API and still leaves a normal server
// disk with room.
//
// ponytail: one global constant, not a per-device quota. Add per-device
// accounting if this ever has more than the handful of devices one owner pairs.
const MaxUploadBytes = 16 << 30

// UploadTTL is how long an untouched partial upload is kept before the next
// Create sweeps it away. Resumable means "survives a dropped connection and a
// daemon restart", not "occupies disk forever": without this, every abandoned
// upload is permanent, and abandoning uploads costs the client nothing.
const UploadTTL = 24 * time.Hour

// uploadState is persisted next to the partial file so an upload survives a
// daemon restart — otherwise "resumable" would only mean "resumable until the
// service blinks".
type uploadState struct {
	ID       string `json:"id"`
	Target   string `json:"target"`
	Length   int64  `json:"length"`
	Expected string `json:"expected_sha256"`
}

// Uploader implements resumable uploads into a Store.
type Uploader struct {
	store *Store
	dir   string

	// mu guards dir bookkeeping and the locks map only. It is explicitly NOT
	// held while a chunk streams: one global mutex around the body copy meant a
	// single slow client blocked every other upload for as long as it cared to
	// dribble bytes — an unauthenticated-looking outage produced by one
	// authenticated device on a bad connection.
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewUploader stores partial uploads in dir.
func NewUploader(store *Store, dir string) (*Uploader, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create upload directory %s: %w", dir, err)
	}
	return &Uploader{store: store, dir: dir, locks: map[string]*sync.Mutex{}}, nil
}

// lockFor returns the mutex serialising writes to one upload. Concurrent
// PATCHes to the *same* upload still have to take turns — they share a file
// offset — but two different uploads no longer wait on each other.
func (u *Uploader) lockFor(id string) *sync.Mutex {
	u.mu.Lock()
	defer u.mu.Unlock()
	m, ok := u.locks[id]
	if !ok {
		m = &sync.Mutex{}
		u.locks[id] = m
	}
	return m
}

func (u *Uploader) forgetLock(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.locks, id)
}

// sweepExpired removes partial uploads whose state file has not been touched
// within UploadTTL. Called from Create, so the cost is paid by whoever is
// starting new uploads rather than by a background goroutine.
func (u *Uploader) sweepExpired() {
	entries, err := os.ReadDir(u.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-UploadTTL)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		os.Remove(u.partPath(id))
		os.Remove(u.statePath(id))
		u.forgetLock(id)
	}
}

// sha256Hex matches exactly what HashFile produces: 64 lowercase hex digits.
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (u *Uploader) partPath(id string) string  { return filepath.Join(u.dir, id+".part") }
func (u *Uploader) statePath(id string) string { return filepath.Join(u.dir, id+".json") }

// Create registers a new upload. target is the destination path relative to the
// store roots; expectedHash is the SHA-256 the client computed *before*
// sending, per Section 8.2 step 1.
func (u *Uploader) Create(id, target string, length int64, expectedHash string) error {
	if length < 0 {
		return errors.New("upload length must not be negative")
	}
	if length > MaxUploadBytes {
		return ErrTooLarge
	}
	// CLAUDE.md point 6 and Section 8.2 make SHA-256 verification part of the
	// contract, not an optimisation: verify before sending AND after receiving.
	// Accepting an upload without a hash quietly opted out of both halves — the
	// server had nothing to compare against, so Finish's check was skipped and
	// the file landed unverified. A required field that is only sometimes
	// required is not a required field.
	expectedHash = strings.ToLower(strings.TrimSpace(expectedHash))
	if !sha256Hex.MatchString(expectedHash) {
		return ErrMissingChecksum
	}
	// Validate the destination before a single byte is accepted. Discovering
	// at completion time that the target was outside the roots would mean
	// having buffered an arbitrary amount of attacker-chosen data first.
	if _, err := u.store.Resolve(target); err != nil {
		return err
	}
	if !validUploadID(id) {
		return errors.New("invalid upload id")
	}

	u.sweepExpired()

	u.mu.Lock()
	defer u.mu.Unlock()

	f, err := os.OpenFile(u.partPath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create partial file for %s: %w", id, err)
	}
	f.Close()

	state := uploadState{ID: id, Target: target, Length: length, Expected: expectedHash}
	blob, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal upload state: %w", err)
	}
	if err := os.WriteFile(u.statePath(id), blob, 0o644); err != nil {
		return fmt.Errorf("write upload state for %s: %w", id, err)
	}
	return nil
}

func (u *Uploader) loadState(id string) (uploadState, error) {
	if !validUploadID(id) {
		return uploadState{}, errors.New("invalid upload id")
	}
	blob, err := os.ReadFile(u.statePath(id))
	if err != nil {
		return uploadState{}, fmt.Errorf("read upload state for %s: %w", id, err)
	}
	var st uploadState
	if err := json.Unmarshal(blob, &st); err != nil {
		return uploadState{}, fmt.Errorf("parse upload state for %s: %w", id, err)
	}
	return st, nil
}

// Offset returns how many bytes of an upload have been stored. This is what
// makes resuming possible: a client that lost its connection asks where it got
// to and continues from there instead of re-sending everything.
func (u *Uploader) Offset(id string) (int64, int64, error) {
	st, err := u.loadState(id)
	if err != nil {
		return 0, 0, err
	}
	info, err := os.Stat(u.partPath(id))
	if err != nil {
		return 0, 0, fmt.Errorf("stat partial file for %s: %w", id, err)
	}
	return info.Size(), st.Length, nil
}

// WriteChunk appends a chunk at offset. It returns the new offset and whether
// the upload is now complete.
func (u *Uploader) WriteChunk(id string, offset int64, r io.Reader) (int64, bool, error) {
	lock := u.lockFor(id)
	lock.Lock()
	defer lock.Unlock()

	st, err := u.loadState(id)
	if err != nil {
		return 0, false, err
	}

	f, err := os.OpenFile(u.partPath(id), os.O_WRONLY, 0o644)
	if err != nil {
		return 0, false, fmt.Errorf("open partial file for %s: %w", id, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, false, fmt.Errorf("stat partial file for %s: %w", id, err)
	}
	// Refusing a mismatched offset is what keeps a retried or duplicated chunk
	// from being appended twice and quietly corrupting the file.
	if info.Size() != offset {
		return info.Size(), false, ErrOffsetConflict
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, false, fmt.Errorf("seek in partial file for %s: %w", id, err)
	}

	// Bounded by what the upload has left, so an over-long body is refused at
	// the byte that would exceed the declaration rather than after it has all
	// been written to disk. Checking the size afterwards, as this used to,
	// meant a client that declared one kilobyte could still stream a terabyte
	// through and only then be told no.
	remaining := st.Length - offset
	if remaining < 0 {
		remaining = 0
	}
	limited := io.LimitReader(r, remaining+1)
	written, err := io.Copy(f, limited)
	if err != nil {
		return offset + written, false, fmt.Errorf("write chunk for %s: %w", id, err)
	}
	newOffset := offset + written

	if newOffset > st.Length {
		// Truncate back to the declared length so the partial file cannot be
		// left holding the one over-limit byte we accepted to detect this.
		_ = f.Truncate(st.Length)
		return st.Length, false, fmt.Errorf("upload %s exceeded its declared length", id)
	}
	return newOffset, newOffset == st.Length, nil
}

// Finish verifies the assembled file and moves it into place.
//
// This is Section 8.2 steps 3 and 4: the server hashes what it actually wrote
// and compares against what the client declared. On a mismatch the file is
// discarded rather than kept — a silently corrupt file is worse than a failed
// upload, because nothing downstream will ever question it. The computed hash
// is returned either way so the client can compare a second time.
func (u *Uploader) Finish(id string) (string, error) {
	lock := u.lockFor(id)
	lock.Lock()
	defer lock.Unlock()
	defer u.forgetLock(id)

	st, err := u.loadState(id)
	if err != nil {
		return "", err
	}

	sum, err := HashFile(u.partPath(id))
	if err != nil {
		return "", err
	}

	// No `st.Expected != ""` escape hatch: Create refuses an upload without a
	// well-formed hash, so by the time anything reaches here there is always
	// something to compare against.
	if !strings.EqualFold(sum, st.Expected) {
		u.discard(id)
		return sum, ErrChecksumMismatch
	}

	target, err := u.store.Resolve(st.Target)
	if err != nil {
		u.discard(id)
		return sum, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return sum, fmt.Errorf("create target directory: %w", err)
	}
	if err := os.Rename(u.partPath(id), target); err != nil {
		// Rename fails across filesystems; fall back to a copy.
		if copyErr := copyFile(u.partPath(id), target); copyErr != nil {
			return sum, fmt.Errorf("move upload into place: %w", copyErr)
		}
		os.Remove(u.partPath(id))
	}
	os.Remove(u.statePath(id))
	return sum, nil
}

// discard removes both the partial file and its state.
func (u *Uploader) discard(id string) {
	os.Remove(u.partPath(id))
	os.Remove(u.statePath(id))
}

// Discard abandons an upload.
func (u *Uploader) Discard(id string) {
	lock := u.lockFor(id)
	lock.Lock()
	u.discard(id)
	lock.Unlock()
	u.forgetLock(id)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// validUploadID keeps an id from turning into a path. Ids are generated by the
// daemon, but they arrive back from the client in the URL, so they are
// untrusted input by the time they are used to build a filename.
func validUploadID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// ParseUploadMetadata decodes a tus Upload-Metadata header:
// comma-separated "key base64value" pairs.
func ParseUploadMetadata(header string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.SplitN(part, " ", 2)
		key := fields[0]
		if len(fields) == 1 {
			out[key] = ""
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(fields[1])
		if err != nil {
			continue
		}
		out[key] = string(decoded)
	}
	return out
}

// ParseOffset reads a tus Upload-Offset / Upload-Length header value.
func ParseOffset(v string) (int64, error) {
	if v == "" {
		return 0, errors.New("missing offset")
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid offset %q", v)
	}
	return n, nil
}
