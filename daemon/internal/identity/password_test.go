package identity

import (
	"errors"
	"testing"
)

func TestPasswordVerifyCorrect(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	v := NewPasswordVerifier(encoded)
	if err := v.Verify([]byte("correct horse battery staple")); err != nil {
		t.Fatalf("Verify(correct password) = %v, want nil", err)
	}
}

func TestPasswordVerifyWrong(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	v := NewPasswordVerifier(encoded)
	err = v.Verify([]byte("wrong password"))
	if !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("Verify(wrong password) = %v, want ErrPasswordMismatch", err)
	}
}

// A fixed salt would make identical passwords hash identically, which turns
// the hash into a lookup-table target. This is the tripwire for that.
func TestHashPasswordUsesRandomSalt(t *testing.T) {
	a, err := HashPassword("same password")
	if err != nil {
		t.Fatalf("HashPassword #1: %v", err)
	}
	b, err := HashPassword("same password")
	if err != nil {
		t.Fatalf("HashPassword #2: %v", err)
	}
	if a == b {
		t.Fatal("hashing the same password twice produced identical encodings: salt is not random")
	}
	// Both must still verify correctly despite differing.
	if err := ComparePassword("same password", a); err != nil {
		t.Fatalf("ComparePassword(a): %v", err)
	}
	if err := ComparePassword("same password", b); err != nil {
		t.Fatalf("ComparePassword(b): %v", err)
	}
}

func TestComparePasswordRejectsMalformedHash(t *testing.T) {
	cases := map[string]string{
		"empty string":        "",
		"garbage":             "not-an-argon2-hash",
		"truncated":           "$argon2id$v=19$m=19456,t=2,p=1$",
		"wrong algorithm tag": "$bcrypt$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		"bad base64 salt":     "$argon2id$v=19$m=19456,t=2,p=1$not-base64!!!$aGFzaA",
		"unparsable params":   "$argon2id$v=19$oops$c2FsdA$aGFzaA",
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			err := ComparePassword("anything", encoded)
			if !errors.Is(err, ErrMalformedHash) {
				t.Fatalf("ComparePassword(%q) = %v, want ErrMalformedHash", encoded, err)
			}
		})
	}
}

func TestHashPasswordRejectsEmptyPassword(t *testing.T) {
	if _, err := HashPassword(""); !errors.Is(err, ErrEmptyPassword) {
		t.Fatalf("HashPassword(\"\") = %v, want ErrEmptyPassword", err)
	}
}
