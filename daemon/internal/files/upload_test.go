package files

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newUploader(t *testing.T) (*Uploader, string) {
	t.Helper()
	s, root := newStore(t)
	u, err := NewUploader(s, filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatalf("NewUploader: %v", err)
	}
	return u, root
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The Definition of Done: an upload aborted part-way can be continued rather
// than restarted.
func TestAbortedUploadResumes(t *testing.T) {
	u, root := newUploader(t)
	payload := []byte("the quick brown fox jumps over the lazy dog")

	if err := u.Create("up1", "resumed.txt", int64(len(payload)), sha256hex(payload)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// First chunk, then "the connection drops".
	off, done, err := u.WriteChunk("up1", 0, bytes.NewReader(payload[:10]))
	if err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if off != 10 || done {
		t.Fatalf("after first chunk: offset=%d done=%v, want 10/false", off, done)
	}

	// The client comes back and asks where it got to.
	stored, length, err := u.Offset("up1")
	if err != nil {
		t.Fatalf("Offset: %v", err)
	}
	if stored != 10 || length != int64(len(payload)) {
		t.Fatalf("Offset = (%d, %d), want (10, %d)", stored, length, len(payload))
	}

	// It continues from there instead of starting over.
	off, done, err = u.WriteChunk("up1", stored, bytes.NewReader(payload[stored:]))
	if err != nil {
		t.Fatalf("second chunk: %v", err)
	}
	if !done || off != int64(len(payload)) {
		t.Fatalf("after second chunk: offset=%d done=%v", off, done)
	}

	sum, err := u.Finish("up1")
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if sum != sha256hex(payload) {
		t.Errorf("server hash = %s, want %s", sum, sha256hex(payload))
	}

	got, err := os.ReadFile(filepath.Join(root, "resumed.txt"))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("assembled file does not match the payload")
	}
}

// The other half of the Definition of Done: a tampered chunk must be rejected,
// and the corrupt file must not survive anywhere.
func TestTamperedContentIsRejectedAndDiscarded(t *testing.T) {
	u, root := newUploader(t)
	honest := []byte("original content")
	tampered := []byte("tampered content")

	if err := u.Create("up2", "tampered.txt", int64(len(tampered)), sha256hex(honest)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := u.WriteChunk("up2", 0, bytes.NewReader(tampered)); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	sum, err := u.Finish("up2")
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Finish = %v, want ErrChecksumMismatch", err)
	}
	// The server still reports what it computed, so the client can see the
	// difference rather than just being told "no".
	if sum != sha256hex(tampered) {
		t.Errorf("server hash = %s, want the hash of what was actually received", sum)
	}

	if _, err := os.Stat(filepath.Join(root, "tampered.txt")); !os.IsNotExist(err) {
		t.Error("a file that failed verification was written into place")
	}
	if _, err := os.Stat(u.partPath("up2")); !os.IsNotExist(err) {
		t.Error("the partial file survived a failed verification")
	}
}

// A retried chunk must not be appended twice.
func TestWrongOffsetIsRefused(t *testing.T) {
	u, _ := newUploader(t)
	payload := []byte("0123456789")

	if err := u.Create("up3", "x.txt", int64(len(payload)), sha256hex(payload)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := u.WriteChunk("up3", 0, bytes.NewReader(payload[:4])); err != nil {
		t.Fatalf("first chunk: %v", err)
	}

	got, _, err := u.WriteChunk("up3", 0, bytes.NewReader(payload[:4]))
	if !errors.Is(err, ErrOffsetConflict) {
		t.Fatalf("re-sending at offset 0 = %v, want ErrOffsetConflict", err)
	}
	if got != 4 {
		t.Errorf("conflict reported offset %d, want the real one (4)", got)
	}
}

func TestUploadExceedingDeclaredLengthFails(t *testing.T) {
	u, _ := newUploader(t)
	if err := u.Create("up4", "y.txt", 4, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := u.WriteChunk("up4", 0, bytes.NewReader([]byte("far too long"))); err == nil {
		t.Fatal("expected writing more than the declared length to fail")
	}
}

// The destination is validated before any bytes are accepted; otherwise an
// attacker-chosen amount of data would be buffered before the refusal.
func TestCreateRejectsTargetOutsideRoots(t *testing.T) {
	u, _ := newUploader(t)
	if err := u.Create("up5", "../../etc/cron.d/evil", 10, ""); err == nil {
		t.Fatal("expected a target outside the roots to be refused at creation")
	}
	if _, err := os.Stat(u.partPath("up5")); !os.IsNotExist(err) {
		t.Error("a partial file was created for a rejected target")
	}
}

// Ids come back from the client in the URL, so they are untrusted by the time
// they are used to build a filename.
func TestInvalidUploadIDsRejected(t *testing.T) {
	u, _ := newUploader(t)
	bad := []string{"", "../escape", "a/b", "with space", strings.Repeat("x", 65)}
	for _, id := range bad {
		if err := u.Create(id, "ok.txt", 1, ""); err == nil {
			t.Errorf("Create with id %q was accepted", id)
		}
	}
}

func TestEmptyExpectedHashSkipsVerification(t *testing.T) {
	u, root := newUploader(t)
	payload := []byte("no hash declared")

	if err := u.Create("up6", "nohash.txt", int64(len(payload)), ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := u.WriteChunk("up6", 0, bytes.NewReader(payload)); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	sum, err := u.Finish("up6")
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if sum != sha256hex(payload) {
		t.Errorf("server hash = %s, want %s", sum, sha256hex(payload))
	}
	if _, err := os.Stat(filepath.Join(root, "nohash.txt")); err != nil {
		t.Errorf("file not in place: %v", err)
	}
}

func TestParseUploadMetadata(t *testing.T) {
	// "filename" -> "report.pdf", "sha256" -> "abc"
	header := "filename cmVwb3J0LnBkZg==,sha256 YWJj,partial"
	got := ParseUploadMetadata(header)

	if got["filename"] != "report.pdf" {
		t.Errorf("filename = %q, want report.pdf", got["filename"])
	}
	if got["sha256"] != "abc" {
		t.Errorf("sha256 = %q, want abc", got["sha256"])
	}
	if _, ok := got["partial"]; !ok {
		t.Error("valueless key should still be present")
	}
}

func TestParseOffset(t *testing.T) {
	if _, err := ParseOffset(""); err == nil {
		t.Error("empty offset should fail")
	}
	if _, err := ParseOffset("-1"); err == nil {
		t.Error("negative offset should fail")
	}
	if _, err := ParseOffset("abc"); err == nil {
		t.Error("non-numeric offset should fail")
	}
	n, err := ParseOffset("42")
	if err != nil || n != 42 {
		t.Errorf("ParseOffset(42) = (%d, %v)", n, err)
	}
}
