// Package files implements the file API from docs/ARCHITECTURE.md Section 8.
//
// Access is restricted to an explicit allowlist of roots — /srv/exchange and
// whatever project directories are shared deliberately. There is no free
// filesystem browser over the whole server, and the daemon runs as the agent
// user so it structurally cannot reach outside those paths anyway. The
// allowlist is the second layer, not the only one.
package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrOutsideRoots is returned for any path that does not resolve inside one of
// the configured roots.
var ErrOutsideRoots = errors.New("path is outside the allowed roots")

// Store serves files from a fixed set of roots.
type Store struct {
	roots []string
}

// NewStore returns a Store over the given roots. Roots are resolved once, so a
// later symlink swap on a root itself cannot silently move the boundary.
func NewStore(roots []string) (*Store, error) {
	if len(roots) == 0 {
		return nil, errors.New("at least one root is required")
	}
	resolved := make([]string, 0, len(roots))
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("resolve root %s: %w", r, err)
		}
		// EvalSymlinks fails if the root does not exist yet; fall back to the
		// absolute path so a Store can be built before /srv/exchange is
		// created, without silently accepting a nonexistent root elsewhere.
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		resolved = append(resolved, filepath.Clean(abs))
	}
	return &Store{roots: resolved}, nil
}

// Roots returns the configured roots.
func (s *Store) Roots() []string { return append([]string(nil), s.roots...) }

// Resolve maps a client-supplied path to an absolute path inside one of the
// roots, or fails.
//
// This is the security boundary of the whole file API. Three things have to
// hold at once, and dropping any one of them reopens traversal:
//
//  1. The path is cleaned first, so "a/../../etc/passwd" collapses before it
//     is compared against anything.
//  2. Symlinks are resolved for the deepest existing ancestor, so a symlink
//     *inside* a root pointing outside it cannot be followed. Checking the
//     unresolved path would let `ln -s /etc /srv/exchange/escape` through.
//  3. The comparison is against root + separator, not a bare string prefix.
//     Otherwise "/srv/exchange-evil" would pass a prefix test for
//     "/srv/exchange".
func (s *Store) Resolve(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) {
		return "", ErrOutsideRoots
	}

	// A path is interpreted relative to the first root unless it is absolute.
	candidate := rel
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.roots[0], candidate)
	}
	candidate = filepath.Clean(candidate)

	real, err := resolveExisting(candidate)
	if err != nil {
		return "", err
	}

	for _, root := range s.roots {
		if real == root || strings.HasPrefix(real, root+string(os.PathSeparator)) {
			return real, nil
		}
	}
	return "", ErrOutsideRoots
}

// resolveExisting resolves symlinks for the longest existing prefix of path and
// re-appends the rest. Plain EvalSymlinks fails outright when the final element
// does not exist yet, which is the normal case for an upload target.
func resolveExisting(path string) (string, error) {
	remainder := ""
	current := path
	for {
		real, err := filepath.EvalSymlinks(current)
		if err == nil {
			if remainder == "" {
				return real, nil
			}
			return filepath.Join(real, remainder), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root without finding anything that
			// exists; nothing to resolve, use the cleaned path as-is.
			return path, nil
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

// Entry describes one directory entry.
type Entry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime int64  `json:"mod_time"`
}

// List returns the contents of a directory inside the roots.
func (s *Store) List(rel string) ([]Entry, error) {
	abs, err := s.Resolve(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", rel, err)
	}

	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name:    e.Name(),
			Size:    info.Size(),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime().Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Open returns a reader for a file plus its size and SHA-256.
//
// The hash is computed by reading the file once before streaming it, so a
// download costs two reads. Section 8.2 wants the client to be able to verify
// what it received, and a hash that only arrives as a trailer after the body
// is useless to a client that has already written the file to disk.
func (s *Store) Open(rel string) (io.ReadCloser, int64, string, error) {
	abs, err := s.Resolve(rel)
	if err != nil {
		return nil, 0, "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, 0, "", fmt.Errorf("stat %s: %w", rel, err)
	}
	if info.IsDir() {
		return nil, 0, "", fmt.Errorf("%s is a directory", rel)
	}

	sum, err := HashFile(abs)
	if err != nil {
		return nil, 0, "", err
	}

	f, err := os.Open(abs)
	if err != nil {
		return nil, 0, "", fmt.Errorf("open %s: %w", rel, err)
	}
	return f, info.Size(), sum, nil
}

// Delete removes a file inside the roots. Directories are refused: a recursive
// delete behind a single API call is a much bigger blast radius than this
// endpoint needs.
func (s *Store) Delete(rel string) error {
	abs, err := s.Resolve(rel)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat %s: %w", rel, err)
	}
	if info.IsDir() {
		return errors.New("refusing to delete a directory")
	}
	if err := os.Remove(abs); err != nil {
		return fmt.Errorf("delete %s: %w", rel, err)
	}
	return nil
}

// HashFile returns the SHA-256 of a file as lowercase hex.
//
// SHA-256 rather than MD5 (decision D-08): MD5 collisions are practical to
// construct, so anyone with write access to an intermediate point could craft
// a file that passes the check. The extra cost is not measurable here.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s for hashing: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
