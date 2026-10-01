// Package auth holds authentication primitives shared by the agent API and
// the admin UI.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// GenerateToken returns a new 256-bit agent token, its storage hash and a
// short display prefix.
func GenerateToken() (token, hash, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", err
	}
	token = "dgw_" + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), token[:12], nil
}

// HashToken is a plain SHA-256: tokens carry 256 bits of entropy, so a slow
// hash adds nothing.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
