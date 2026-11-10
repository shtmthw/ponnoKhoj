// Package auth implements vendor/admin accounts: argon2id password hashing,
// opaque server-side sessions, and login lockout.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. These follow the OWASP Password Storage Cheat Sheet's
// minimum recommended configuration (m=19 MiB, t=2, p=1). They are encoded in
// every hash, so they can be raised later without invalidating old hashes.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonSaltLen   = 16
	argonKeyLen    = 32
)

// Password policy bounds. The upper bound stops hashing-cost abuse.
const (
	MinPasswordRunes = 10
	MaxPasswordBytes = 256
)

// Password errors.
var (
	ErrPasswordTooShort = errors.New("auth: password must be at least 10 characters")
	ErrPasswordTooLong  = errors.New("auth: password must be at most 256 bytes")
	errMalformedHash    = errors.New("auth: malformed password hash")
)

// CheckPasswordPolicy validates a new password's length.
func CheckPasswordPolicy(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordRunes {
		return ErrPasswordTooShort
	}
	if len(password) > MaxPasswordBytes {
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword returns an argon2id hash in PHC string format:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the PHC-encoded hash. It
// compares in constant time and returns an error only for a malformed hash.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errMalformedHash
	}
	var ver int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &ver); err != nil || ver != argon2.Version {
		return false, errMalformedHash
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, errMalformedHash
	}
	// Bound the parameters so a corrupted row cannot demand absurd work.
	if memory < 8 || memory > 1<<20 || time < 1 || time > 16 || threads < 1 || threads > 16 {
		return false, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 128 {
		return false, errMalformedHash
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want))) //nolint:gosec // len(want) is bounded to [16, 128] above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
