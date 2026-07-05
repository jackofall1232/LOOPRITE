// Package util holds the small cross-cutting helpers ported from util.js: id generation,
// UTC timestamps (millisecond ISO, matching JS Date.toISOString so lexicographic time
// comparisons still work), constant-time string comparison, and the rough token estimate.
package util

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math"
	"time"
)

// NowISO returns the current UTC time in the same millisecond ISO-8601 format that
// JavaScript's Date.toISOString() produces (e.g. "2026-07-04T12:34:56.789Z"). The exact
// format matters: reservation reaping and ledger ordering compare these values as strings,
// which is only correct when every writer uses the identical layout.
func NowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// ISOFromTime formats an arbitrary time the same way (used for cutoffs / lease expiry).
func ISOFromTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// UTCDay returns YYYY-MM-DD in UTC for the current instant.
func UTCDay() string { return time.Now().UTC().Format("2006-01-02") }

// RID builds a random id "<prefix>_<18 hex chars>" (9 random bytes), matching rid() in util.js.
func RID(prefix string) string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is fatal-worthy, but keep the signature simple; fall back to time.
		return fmt.Sprintf("%s_%x", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// SHA256Hex returns the lowercase hex sha-256 of s.
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TimingSafeEqual compares two strings in constant time (length-independent short-circuit only
// on unequal length, exactly like the Node helper). Uses crypto/subtle so a secret/token check
// never leaks timing — never regress this to ==.
func TimingSafeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// EstimateTokensFromChars is the rough pre-flight estimate ONLY (reservation ceiling): ceil(chars/3.5).
func EstimateTokensFromChars(chars int) int {
	return int(math.Ceil(float64(chars) / 3.5))
}
