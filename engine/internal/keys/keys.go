// Package keys validates user-supplied names and derives placement hashes.
package keys

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on user-supplied identifiers.
const (
	MaxKeyLen      = 1024
	MinBucketLen   = 3
	MaxBucketLen   = 63
	MaxMetaEntries = 32
	MaxMetaKeyLen  = 64
	MaxMetaValLen  = 512
)

// ErrInvalid is wrapped by every validation failure.
var ErrInvalid = errors.New("invalid name")

// ValidateBucket accepts lowercase DNS-style names such as "scans-2026".
func ValidateBucket(name string) error {
	if len(name) < MinBucketLen || len(name) > MaxBucketLen {
		return fmt.Errorf("%w: bucket name must be %d-%d characters", ErrInvalid, MinBucketLen, MaxBucketLen)
	}
	for i, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '-' && i > 0 && i < len(name)-1)
		if !ok {
			return fmt.Errorf("%w: bucket name may contain only a-z, 0-9 and inner hyphens", ErrInvalid)
		}
	}
	return nil
}

// ValidateKey accepts any printable UTF-8 string up to MaxKeyLen bytes.
// Keys are never used as file paths, so "/" and ".." are allowed.
func ValidateKey(key string) error {
	if key == "" || len(key) > MaxKeyLen {
		return fmt.Errorf("%w: key must be 1-%d bytes", ErrInvalid, MaxKeyLen)
	}
	if !utf8.ValidString(key) {
		return fmt.Errorf("%w: key must be valid UTF-8", ErrInvalid)
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: key must not contain control characters", ErrInvalid)
		}
	}
	return nil
}

// ValidateMetadata bounds user metadata attached to an object.
func ValidateMetadata(meta map[string]string) error {
	if len(meta) > MaxMetaEntries {
		return fmt.Errorf("%w: at most %d metadata entries", ErrInvalid, MaxMetaEntries)
	}
	for k, v := range meta {
		if k == "" || len(k) > MaxMetaKeyLen || len(v) > MaxMetaValLen {
			return fmt.Errorf("%w: metadata %q exceeds size limits", ErrInvalid, k)
		}
		if strings.IndexFunc(k, func(r rune) bool {
			return !(r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
		}) >= 0 {
			return fmt.Errorf("%w: metadata key %q may contain only a-z, 0-9, - and _", ErrInvalid, k)
		}
		if !utf8.ValidString(v) {
			return fmt.Errorf("%w: metadata value for %q must be UTF-8", ErrInvalid, k)
		}
	}
	return nil
}

// ValidateHash accepts a lowercase hex SHA-256 digest.
func ValidateHash(h string) error {
	if len(h) != sha256.Size*2 {
		return fmt.Errorf("%w: hash must be %d hex characters", ErrInvalid, sha256.Size*2)
	}
	for _, r := range h {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return fmt.Errorf("%w: hash must be lowercase hex", ErrInvalid)
		}
	}
	return nil
}

// ObjectHash is the placement hash of an object.
func ObjectHash(bucket, key string) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(bucket))
	h.Write([]byte{0})
	h.Write([]byte(key))
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// HashHex returns the lowercase hex SHA-256 of data.
func HashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
