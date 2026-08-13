package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters for the owner password (Section 10.2, factor 1;
// Argon2id chosen over bcrypt per the same section, for its resistance to
// GPU-assisted attacks). These sit at the low end of OWASP's Password
// Storage Cheat Sheet range for Argon2id (memory >= 19 MiB, iterations = 2,
// parallelism = 1): this daemon shares a small homelab/VPS box with the
// terminal sessions it serves, and a heavier setting would compete with
// those for memory on every login. The parameters are encoded into every
// hash produced by HashPassword, so raising them later does not invalidate
// hashes already stored — only re-hashing on next successful login would.
const (
	argonMemoryKiB   = 19 * 1024 // 19 MiB
	argonIterations  = 2
	argonParallelism = 1
	argonSaltLen     = 16
	argonKeyLen      = 32
	argon2idVersion  = argon2.Version
)

var (
	// ErrEmptyPassword is returned by HashPassword for an empty password: an
	// "empty password hashes fine, just don't use it" API is how empty
	// passwords end up in production.
	ErrEmptyPassword = errors.New("password must not be empty")
	// ErrMalformedHash is returned when a stored encoded hash cannot be
	// parsed. It must never be treated as a match.
	ErrMalformedHash = errors.New("stored password hash is malformed")
	// ErrPasswordMismatch is returned when the password does not match the
	// stored hash.
	ErrPasswordMismatch = errors.New("password does not match")
)

// HashPassword returns an Argon2id hash of password, encoded in the
// standard "$argon2id$v=19$m=...,t=...,p=...$salt$hash" form so the
// parameters and a fresh random salt travel with the hash itself.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", ErrEmptyPassword
	}
	salt := make([]byte, argonSaltLen)
	// crypto/rand, never a fixed salt: a fixed salt turns Argon2id's whole
	// point — unique per-password work factor — into a lookup-table target.
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonParallelism, argonKeyLen)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2idVersion, argonMemoryKiB, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
	return encoded, nil
}

// ComparePassword checks password against an encoded Argon2id hash produced
// by HashPassword. It returns ErrMalformedHash if encoded cannot be parsed —
// never a false match — and ErrPasswordMismatch if it parses but does not
// match.
func ComparePassword(password, encoded string) error {
	params, salt, hash, err := decodeArgon2id(encoded)
	if err != nil {
		return err
	}
	candidate := argon2.IDKey([]byte(password), salt, params.iterations, params.memoryKiB, params.parallelism, uint32(len(hash)))
	// crypto/subtle.ConstantTimeCompare, never ==: a timing-variable
	// comparison on a password hash leaks how many leading bytes matched,
	// which is a real side channel and not a theoretical one.
	if subtle.ConstantTimeCompare(candidate, hash) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}

// PasswordVerifier implements Verifier for the pairing chain's first factor
// (Section 10.2: email + password). Evidence is the raw UTF-8 password.
type PasswordVerifier struct {
	encoded string
}

// NewPasswordVerifier builds a verifier against a previously stored encoded
// hash (see HashPassword).
func NewPasswordVerifier(encodedHash string) *PasswordVerifier {
	return &PasswordVerifier{encoded: encodedHash}
}

// Verify reports nil only if evidence, interpreted as a password, matches
// the stored hash.
func (p *PasswordVerifier) Verify(evidence []byte) error {
	return ComparePassword(string(evidence), p.encoded)
}

type argon2Params struct {
	memoryKiB   uint32
	iterations  uint32
	parallelism uint8
}

// decodeArgon2id parses the "$argon2id$v=...$m=...,t=...,p=...$salt$hash"
// form. Any deviation — wrong field count, wrong algorithm tag, unparsable
// numbers, undecodable base64, an empty salt or hash — is ErrMalformedHash.
// A malformed or truncated hash must fail, never panic and never "match".
func decodeArgon2id(encoded string) (argon2Params, []byte, []byte, error) {
	// "$argon2id$v=19$m=...,t=...,p=...$salt$hash" splits into 6 parts on
	// "$", the first being empty (the string starts with the separator).
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return argon2Params{}, nil, nil, fmt.Errorf("%w: unexpected format", ErrMalformedHash)
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return argon2Params{}, nil, nil, fmt.Errorf("%w: version: %v", ErrMalformedHash, err)
	}
	if version != argon2idVersion {
		return argon2Params{}, nil, nil, fmt.Errorf("%w: unsupported version %d", ErrMalformedHash, version)
	}

	var p argon2Params
	var parallelism int
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memoryKiB, &p.iterations, &parallelism); err != nil {
		return argon2Params{}, nil, nil, fmt.Errorf("%w: params: %v", ErrMalformedHash, err)
	}
	p.parallelism = uint8(parallelism)

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return argon2Params{}, nil, nil, fmt.Errorf("%w: salt: %v", ErrMalformedHash, err)
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return argon2Params{}, nil, nil, fmt.Errorf("%w: hash: %v", ErrMalformedHash, err)
	}
	if len(salt) == 0 || len(hash) == 0 {
		return argon2Params{}, nil, nil, fmt.Errorf("%w: empty salt or hash", ErrMalformedHash)
	}

	return p, salt, hash, nil
}
