package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

type argonHash struct {
	m, t      uint32
	p         uint8
	salt, key []byte
}

// parseHash decodes a PHC-encoded argon2id hash. Parameters are bounded so a
// bad hash in config cannot make login exhaust memory.
func parseHash(encoded string) (argonHash, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return argonHash{}, errors.New("not an argon2id hash of this version")
	}
	var h argonHash
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &h.m, &h.t, &h.p); err != nil {
		return argonHash{}, errors.New("malformed parameters")
	}
	if h.m < 8*1024 || h.m > 1<<20 || h.t < 1 || h.t > 10 || h.p < 1 || h.p > 16 {
		return argonHash{}, errors.New("parameters out of range")
	}
	var err error
	if h.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(h.salt) < 8 {
		return argonHash{}, errors.New("malformed salt")
	}
	if h.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(h.key) < 16 || len(h.key) > 64 {
		return argonHash{}, errors.New("malformed key")
	}
	return h, nil
}

// ValidPasswordHash reports why encoded cannot be used to check passwords.
func ValidPasswordHash(encoded string) error {
	_, err := parseHash(encoded)
	return err
}

// CheckPassword verifies pw against a PHC-encoded argon2id hash.
func CheckPassword(encoded, pw string) bool {
	h, err := parseHash(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), h.salt, h.t, h.m, h.p, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(got, h.key) == 1
}
