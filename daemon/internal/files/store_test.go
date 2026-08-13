package files

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	// EvalSymlinks matters here: on macOS t.TempDir() is under /var, which is
	// itself a symlink to /private/var, so the resolved root differs from the
	// literal one.
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	s, err := NewStore([]string{real})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, real
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The allowlist is the security boundary of the whole file API. Each of these
// is a real technique, not a hypothetical.
func TestResolveRejectsTraversal(t *testing.T) {
	s, root := newStore(t)
	write(t, filepath.Join(root, "ok.txt"), "fine")

	bad := []string{
		"../etc/passwd",
		"../../etc/passwd",
		"sub/../../outside.txt",
		"/etc/passwd",
		"a/b/../../../../../../etc/passwd",
		"./../../",
	}
	for _, p := range bad {
		if got, err := s.Resolve(p); err == nil {
			t.Errorf("Resolve(%q) = %q, want an error", p, got)
		}
	}
}

// A prefix comparison without the separator would accept a sibling directory
// whose name merely starts with the root's name.
func TestResolveRejectsSiblingWithSharedPrefix(t *testing.T) {
	base := t.TempDir()
	real, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	root := filepath.Join(real, "exchange")
	evil := filepath.Join(real, "exchange-evil")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(evil, "secret.txt"), "nope")

	s, err := NewStore([]string{root})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got, err := s.Resolve(filepath.Join(evil, "secret.txt")); err == nil {
		t.Fatalf("Resolve escaped into a shared-prefix sibling: %q", got)
	}
}

// A symlink inside a root pointing outside it is the case that a naive
// Clean-and-compare misses entirely.
func TestResolveRefusesToFollowSymlinkOutOfRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s, root := newStore(t)

	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "top secret")

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if got, err := s.Resolve("escape/secret.txt"); err == nil {
		t.Fatalf("followed a symlink out of the root to %q", got)
	} else if !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("got %v, want ErrOutsideRoots", err)
	}
}

func TestResolveAcceptsPathsInsideRoot(t *testing.T) {
	s, root := newStore(t)
	write(t, filepath.Join(root, "sub", "file.txt"), "hello")

	got, err := s.Resolve("sub/file.txt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != filepath.Join(root, "sub", "file.txt") {
		t.Errorf("Resolve = %q, want %q", got, filepath.Join(root, "sub", "file.txt"))
	}
}

// An upload target does not exist yet, so resolution must work for a path
// whose final element is missing.
func TestResolveWorksForNonexistentTarget(t *testing.T) {
	s, root := newStore(t)
	got, err := s.Resolve("not-there-yet.bin")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != filepath.Join(root, "not-there-yet.bin") {
		t.Errorf("Resolve = %q, want %q", got, filepath.Join(root, "not-there-yet.bin"))
	}
}

func TestListSortsDirectoriesFirst(t *testing.T) {
	s, root := newStore(t)
	write(t, filepath.Join(root, "b.txt"), "b")
	write(t, filepath.Join(root, "a.txt"), "a")
	if err := os.MkdirAll(filepath.Join(root, "zdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := s.List(".")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if !entries[0].IsDir || entries[0].Name != "zdir" {
		t.Errorf("expected the directory first, got %+v", entries[0])
	}
	if entries[1].Name != "a.txt" || entries[2].Name != "b.txt" {
		t.Errorf("files not sorted by name: %+v", entries[1:])
	}
}

func TestOpenReturnsSizeAndHash(t *testing.T) {
	s, root := newStore(t)
	write(t, filepath.Join(root, "f.txt"), "hello")

	rc, size, sum, err := s.Open("f.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()

	if size != 5 {
		t.Errorf("size = %d, want 5", size)
	}
	// Known SHA-256 of "hello".
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if sum != want {
		t.Errorf("sha256 = %s, want %s", sum, want)
	}
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
}

func TestDeleteRefusesDirectories(t *testing.T) {
	s, root := newStore(t)
	if err := os.MkdirAll(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("adir"); err == nil {
		t.Fatal("expected Delete to refuse a directory")
	}
	if _, err := os.Stat(filepath.Join(root, "adir")); err != nil {
		t.Fatalf("directory was removed despite the refusal: %v", err)
	}
}

func TestDeleteRemovesFile(t *testing.T) {
	s, root := newStore(t)
	write(t, filepath.Join(root, "gone.txt"), "x")
	if err := s.Delete("gone.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still present after Delete: %v", err)
	}
}

func TestDeleteOutsideRootIsRefused(t *testing.T) {
	s, _ := newStore(t)
	outside := filepath.Join(t.TempDir(), "victim.txt")
	write(t, outside, "do not delete me")

	if err := s.Delete(outside); err == nil {
		t.Fatal("expected Delete outside the roots to fail")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("file outside the root was deleted: %v", err)
	}
}

func TestNewStoreRequiresRoots(t *testing.T) {
	if _, err := NewStore(nil); err == nil {
		t.Fatal("expected NewStore to reject an empty root list")
	}
}

func TestResolveRejectsNullByte(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Resolve("ok\x00.txt"); err == nil {
		t.Fatal("expected a null byte in the path to be rejected")
	}
}

func TestMultipleRoots(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	realA, _ := filepath.EvalSymlinks(a)
	realB, _ := filepath.EvalSymlinks(b)
	write(t, filepath.Join(realB, "in-b.txt"), "b")

	s, err := NewStore([]string{realA, realB})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	got, err := s.Resolve(filepath.Join(realB, "in-b.txt"))
	if err != nil {
		t.Fatalf("Resolve in second root: %v", err)
	}
	if !strings.HasPrefix(got, realB) {
		t.Errorf("resolved to %q, expected it under %q", got, realB)
	}
}
