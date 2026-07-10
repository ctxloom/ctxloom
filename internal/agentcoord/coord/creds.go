package coord

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"time"
)

// randID mints a short random hex id with prefix and n random bytes, falling
// back to a nanosecond stamp on the (astronomically unlikely) rand failure —
// uniqueness matters, cryptographic quality does not. Shared by the run-id and
// message-id minters (identical shape).
func randID(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)
}

// mintToken mints a 256-bit bearer credential, returning the token (hex,
// handed to exactly one runner via the env seam and never persisted) and the
// hex SHA-256 hash that IS persisted (in the run-registry journal).
func mintToken() (token, hash string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("coord: mint credential: %w", err)
	}
	token = hex.EncodeToString(b[:])
	return token, hashToken(token), nil
}

// hashToken is the persisted form of a bearer token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// verifyToken resolves a presented bearer token against the active credential
// set in CONSTANT TIME per candidate: the presented token is hashed once,
// then compared against every active hash with subtle.ConstantTimeCompare and
// no early exit, so timing reveals nothing about which (if any) credential
// matched. The active set is small (one per live run + session owners).
func verifyToken(presented string, active map[string]Identity) (Identity, bool) {
	sum := sha256.Sum256([]byte(presented))
	var (
		match Identity
		found int
	)
	for hash, id := range active {
		raw, err := hex.DecodeString(hash)
		if err != nil || len(raw) != sha256.Size {
			continue
		}
		if subtle.ConstantTimeCompare(raw, sum[:]) == 1 {
			match = id
			found = 1
		}
	}
	return match, found == 1
}
